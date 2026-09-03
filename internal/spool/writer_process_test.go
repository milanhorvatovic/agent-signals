package spool

import (
	"bufio"
	"bytes"
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
	holderModeEnv   = "AGENT_SIGNALS_TEST_HOLDER_MODE"
	heldLine        = "HELD"

	holdMode   = "hold"
	appendMode = "append"
)

// TestMain doubles as a writer subprocess. The guard is only worth testing
// against a real second process, and a second process is also the only way to
// observe what a writer leaves behind when it dies — mid-hold or mid-append.
func TestMain(m *testing.M) {
	if root := os.Getenv(holderRootEnv); root != "" {
		source := os.Getenv(holderSourceEnv)
		if os.Getenv(holderModeEnv) == appendMode {
			appendUntilKilled(Root(root), source)
		}
		holdWriter(Root(root), source)
	}
	os.Exit(m.Run())
}

// crashRecord is large enough that a partially copied write would be visible
// in the file, and identical on every append so that any complete line must
// equal it exactly.
func crashRecord() []byte {
	record := make([]byte, 64*1024)
	for i := range record {
		record[i] = 'x'
	}

	return record
}

func appendUntilKilled(root Root, source string) {
	syncer, err := durability.Probe(string(root))
	if err != nil {
		os.Exit(1)
	}
	writer, err := Open(root, source, syncer)
	if err != nil {
		os.Exit(1)
	}

	if _, err := os.Stdout.WriteString(heldLine + "\n"); err != nil {
		os.Exit(1)
	}

	record := crashRecord()
	for {
		if err := writer.Append(record); err != nil {
			os.Exit(1)
		}
	}
}

func holdWriter(root Root, source string) {
	syncer, err := durability.Probe(string(root))
	if err != nil {
		os.Exit(1)
	}
	writer, err := Open(root, source, syncer)
	if err != nil {
		os.Exit(1)
	}

	if _, err := os.Stdout.WriteString(heldLine + "\n"); err != nil {
		os.Exit(1)
	}
	// The parent closes our stdin to order a clean release; being killed
	// instead is the case the test is really after. Closing the writer here
	// rather than dropping it also keeps the lock descriptor reachable for as
	// long as this process claims to hold the guard.
	_, _ = io.Copy(io.Discard, os.Stdin)
	_ = writer.Close()
	os.Exit(0)
}

func startHoldingProcess(t *testing.T, root Root, source string) *exec.Cmd {
	t.Helper()

	return startWriterProcess(t, holdMode, root, source)
}

func startWriterProcess(t *testing.T, mode string, root Root, source string) *exec.Cmd {
	t.Helper()

	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(),
		holderRootEnv+"="+string(root),
		holderSourceEnv+"="+source,
		holderModeEnv+"="+mode,
	)
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

// splitRecords reports how many complete copies of record the spool holds and
// how long the unterminated tail is, failing if the file is in any other
// shape: a complete line that is not the record means two appends interleaved
// or one was truncated in place, and a tail that is not a prefix of the record
// means the fragment came from somewhere other than an interrupted append.
func splitRecords(t *testing.T, content, record []byte) (complete int, tail int) {
	t.Helper()

	rest := content
	for {
		end := bytes.IndexByte(rest, '\n')
		if end < 0 {
			break
		}
		if !bytes.Equal(rest[:end], record) {
			t.Fatalf("record %d is %d bytes, want an intact %d-byte record", complete, end, len(record))
		}
		complete++
		rest = rest[end+1:]
	}
	if !bytes.HasPrefix(record, rest) {
		t.Fatalf("the %d-byte tail is not the beginning of a record", len(rest))
	}

	return complete, len(rest)
}

// Killing a writer while it appends is the process-crash half of the
// durability claim, and the half a machine can actually check. The other half,
// host loss, cannot be reproduced on a CI runner: the platform primitive is
// verified by the durability adapter instead, and the guarantee itself needs
// validating by hand on real hardware.
//
// Note that a kill rarely tears a record: the kernel copies a write into the
// page cache without a preemption point that would drop half of it, so the
// crash usually lands between appends or inside the sync that follows one.
// What this pins is that no crash leaves the spool in a shape a reader could
// not handle, and that the next writer recovers whatever it finds. The seeded
// fragments in TestOpenRepairsATornTail drive the repair path itself.
func TestKillingAWriterMidAppendLeavesARecoverableSpool(t *testing.T) {
	record := crashRecord()

	// Varying delays move the kill around the append loop without the
	// flakiness of a random one.
	for _, delay := range []time.Duration{3 * time.Millisecond, 11 * time.Millisecond, 29 * time.Millisecond, 53 * time.Millisecond, 79 * time.Millisecond} {
		t.Run(delay.String(), func(t *testing.T) {
			root, syncer := newRoot(t)
			// startWriterProcess returns once the child reports the guard,
			// so the delay below is time spent appending, not starting up.
			writer := startWriterProcess(t, appendMode, root, "pr-comments")

			time.Sleep(delay)
			if err := writer.Process.Kill(); err != nil {
				t.Fatalf("kill writer: %v", err)
			}
			_ = writer.Wait()

			crashed, err := os.ReadFile(root.eventsPath("pr-comments"))
			if err != nil {
				t.Fatalf("read spool after the crash: %v", err)
			}
			complete, tail := splitRecords(t, crashed, record)
			t.Logf("crash left %d complete records and a %d-byte tail", complete, tail)

			reopened := openWriterAfterCrash(t, root, syncer)
			repaired, err := os.ReadFile(root.eventsPath("pr-comments"))
			if err != nil {
				t.Fatalf("read spool after the repair: %v", err)
			}
			if repairedComplete, repairedTail := splitRecords(t, repaired, record); repairedComplete != complete || repairedTail != 0 {
				t.Fatalf("repair left %d records and a %d-byte tail, want %d and 0", repairedComplete, repairedTail, complete)
			}

			// The point of the repair: what follows a crash is framed as its
			// own record rather than fused onto the fragment.
			const resumed = `{"id":"after-the-crash"}`
			if err := reopened.Append([]byte(resumed)); err != nil {
				t.Fatalf("append after the crash: %v", err)
			}
			after, err := os.ReadFile(root.eventsPath("pr-comments"))
			if err != nil {
				t.Fatalf("read spool after the append: %v", err)
			}
			if want := string(repaired) + resumed + "\n"; string(after) != want {
				t.Fatalf("the resumed append produced %d bytes, want %d", len(after), len(want))
			}
		})
	}
}

func openWriterAfterCrash(t *testing.T, root Root, syncer durability.Syncer) *Writer {
	t.Helper()

	writer, err := Open(root, "pr-comments", syncer)
	if err != nil {
		t.Fatalf("open after the crash: %v", err)
	}
	t.Cleanup(func() { _ = writer.Close() })

	return writer
}
