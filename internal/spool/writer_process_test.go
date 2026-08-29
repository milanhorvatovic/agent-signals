package spool

import (
	"bufio"
	"errors"
	"io"
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"

	"github.com/milanhorvatovic/agent-signals/internal/durability"
)

const (
	holderRootEnv   = "AGENT_SIGNALS_TEST_HOLDER_ROOT"
	holderSourceEnv = "AGENT_SIGNALS_TEST_HOLDER_SOURCE"
	heldLine        = "HELD"
)

// TestMain doubles as a writer-holding subprocess. The guard is only worth
// testing against a real second process, and a second process is also the
// only way to observe what happens when a writer dies mid-hold.
func TestMain(m *testing.M) {
	if root := os.Getenv(holderRootEnv); root != "" {
		holdWriter(Root(root), os.Getenv(holderSourceEnv))
	}
	os.Exit(m.Run())
}

func holdWriter(root Root, source string) {
	syncer, err := durability.Probe(string(root))
	if err != nil {
		os.Exit(1)
	}
	if _, err := Open(root, source, syncer); err != nil {
		os.Exit(1)
	}

	if _, err := os.Stdout.WriteString(heldLine + "\n"); err != nil {
		os.Exit(1)
	}
	// The parent closes our stdin to order a clean release; being killed
	// instead is the case the test is really after.
	_, _ = io.Copy(io.Discard, os.Stdin)
	os.Exit(0)
}

func startHoldingProcess(t *testing.T, root Root, source string) *exec.Cmd {
	t.Helper()

	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), holderRootEnv+"="+string(root), holderSourceEnv+"="+source)
	cmd.Stderr = os.Stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	if _, err := cmd.StdinPipe(); err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start holder: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	held := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		if scanner.Scan() {
			held <- scanner.Text()
			return
		}
		held <- ""
	}()

	select {
	case line := <-held:
		if line != heldLine {
			t.Fatalf("holder failed to take the guard (got %q)", line)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("holder timed out taking the guard")
	}

	return cmd
}

// A writer that dies still holds an open descriptor until the kernel reaps it,
// which is exactly why the guard is an advisory lock rather than a PID file:
// no stale-lock recovery, no liveness heuristic.
func TestACrashedWriterReleasesTheGuard(t *testing.T) {
	root, syncer := newRoot(t)
	holder := startHoldingProcess(t, root, "pr-comments")

	if _, err := Open(root, "pr-comments", syncer); !errors.Is(err, ErrLocked) {
		t.Fatalf("opened a spool another process holds: %v", err)
	}

	if err := holder.Process.Kill(); err != nil {
		t.Fatalf("kill holder: %v", err)
	}
	_ = holder.Wait()

	deadline := time.Now().Add(10 * time.Second)
	for {
		writer, err := Open(root, "pr-comments", syncer)
		if err == nil {
			_ = writer.Close()
			return
		}
		if !errors.Is(err, ErrLocked) {
			t.Fatalf("open after crash: %v", err)
		}
		if time.Now().After(deadline) {
			t.Fatal("guard still held after the writer was killed")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// The lock lives on the descriptor, so the writer has to keep its lock file
// reachable: were the handle collectable, its finalizer would close the
// descriptor and hand the guard to the next writer while this one still
// believes it owns the source.
func TestWriterKeepsItsLockReachable(t *testing.T) {
	root, syncer := newRoot(t)
	writer := openWriter(t, root, "pr-comments", syncer)

	runtime.GC()
	runtime.GC()

	if _, err := Open(root, "pr-comments", syncer); !errors.Is(err, ErrLocked) {
		t.Fatalf("guard released after garbage collection: %v", err)
	}
	if err := writer.Append([]byte(`{"id":"a"}`)); err != nil {
		t.Fatalf("append after garbage collection: %v", err)
	}
}
