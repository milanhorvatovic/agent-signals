package fixturegen

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"testing"

	"github.com/milanhorvatovic/agent-signals/internal/contract"
)

func TestWindowOverflowStreamIsDeterministicAndCrossesWindow(t *testing.T) {
	var first, second bytes.Buffer
	events, total, err := WindowOverflowStream(&first, "overflow-stream")
	if err != nil {
		t.Fatal(err)
	}
	if total <= WindowBytes {
		t.Fatalf("stream is %d bytes, must cross the %d window", total, WindowBytes)
	}
	if int64(first.Len()) != total {
		t.Fatalf("reported %d bytes, wrote %d", total, first.Len())
	}
	if events < 2 {
		t.Fatalf("want many events, got %d", events)
	}
	if _, _, err := WindowOverflowStream(&second, "overflow-stream"); err != nil {
		t.Fatal(err)
	}
	if sha256.Sum256(first.Bytes()) != sha256.Sum256(second.Bytes()) {
		t.Fatal("generator must be deterministic")
	}

	// Every generated line must satisfy the watcher-input parse profile.
	scanner := bufio.NewScanner(&first)
	scanner.Buffer(nil, contract.DefaultMaxEventBytes)
	checked := 0
	for scanner.Scan() && checked < 5 {
		if _, _, err := contract.ParseEventLine(scanner.Bytes(), 0, contract.WatcherInput); err != nil {
			t.Fatalf("generated line invalid: %v", err)
		}
		checked++
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestDuplicateReplaySet(t *testing.T) {
	const n = 1000 // deliberately larger than any plausible hot cache
	var buf bytes.Buffer
	if err := DuplicateReplaySet(&buf, "replay", n); err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSuffix(buf.Bytes(), []byte("\n")), []byte("\n"))
	if len(lines) != 2*n {
		t.Fatalf("got %d lines, want %d", len(lines), 2*n)
	}
	for i := 0; i < n; i++ {
		if !bytes.Equal(lines[i], lines[n+i]) {
			t.Fatalf("replay line %d is not byte-identical to its original", i)
		}
	}
}
