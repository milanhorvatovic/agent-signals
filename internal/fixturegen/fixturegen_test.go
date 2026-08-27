package fixturegen

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"errors"
	"strings"
	"testing"

	"github.com/milanhorvatovic/agent-signals/internal/contract"
)

var errWriterFull = errors.New("writer is full")

// partialWriter takes every write whole until failOn, where it takes half
// the bytes and fails — the n > 0 alongside an error that io.Writer permits
// and a full disk actually produces.
type partialWriter struct {
	failOn   int
	calls    int
	accepted int64
}

func (p *partialWriter) Write(b []byte) (int, error) {
	p.calls++
	if p.calls < p.failOn {
		p.accepted += int64(len(b))
		return len(b), nil
	}
	n := len(b) / 2
	p.accepted += int64(n)
	return n, errWriterFull
}

func TestPendingOverflowStreamIsDeterministicAndCrossesCap(t *testing.T) {
	var first, second bytes.Buffer
	events, total, err := PendingOverflowStream(&first, "overflow-stream")
	if err != nil {
		t.Fatal(err)
	}
	if total <= PendingBytes {
		t.Fatalf("stream is %d bytes, must cross the %d cap", total, PendingBytes)
	}
	if int64(first.Len()) != total {
		t.Fatalf("reported %d bytes, wrote %d", total, first.Len())
	}
	if events < 2 {
		t.Fatalf("want many events, got %d", events)
	}

	// The crossing must happen before the final event, not on it: the
	// promised event past the cap is the one the supervisor has to drop.
	body := first.Bytes()
	beforeLastEvent := int64(bytes.LastIndex(body[:len(body)-1], []byte("\n")) + 1)
	if beforeLastEvent <= PendingBytes {
		t.Fatalf("stream reaches %d bytes before its final event, so it crosses the %d cap only on that event", beforeLastEvent, PendingBytes)
	}

	if _, _, err := PendingOverflowStream(&second, "overflow-stream"); err != nil {
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

func TestPendingOverflowStreamCountsPartiallyWrittenBytes(t *testing.T) {
	w := &partialWriter{failOn: 2}
	events, total, err := PendingOverflowStream(w, "overflow-stream")
	if !errors.Is(err, errWriterFull) {
		t.Fatalf("want the writer's error, got %v", err)
	}
	if total != w.accepted {
		t.Errorf("reported %d bytes, writer accepted %d — the half-written event is unaccounted for", total, w.accepted)
	}
	if events != 1 {
		t.Errorf("counted %d whole events, want 1 — the failed write produced no complete event", events)
	}
}

func TestDuplicateReplaySetRejectsANegativeCount(t *testing.T) {
	var buf bytes.Buffer
	if err := DuplicateReplaySet(&buf, "replay", -1); err == nil {
		t.Error("DuplicateReplaySet(-1): want an error, got none — an empty corpus is not a generated replay set")
	}
	if buf.Len() != 0 {
		t.Errorf("count was rejected but %d bytes were written", buf.Len())
	}
}

func TestGeneratorsRejectNonSlugSourceBeforeWriting(t *testing.T) {
	// A source is interpolated into two JSON string fields, so anything but
	// a canonical slug yields malformed or schema-invalid events. 129 is a
	// literal, not MaxSlugLen+1: deriving it would move with the constant.
	sources := []string{"", `bad"source`, "Upper", "-leading-dash", strings.Repeat("s", 129)}
	for _, source := range sources {
		var buf bytes.Buffer
		if _, _, err := PendingOverflowStream(&buf, source); err == nil {
			t.Errorf("PendingOverflowStream(%q): want an error, got none", source)
		}
		if err := DuplicateReplaySet(&buf, source, 1); err == nil {
			t.Errorf("DuplicateReplaySet(%q): want an error, got none", source)
		}
		if buf.Len() != 0 {
			t.Errorf("source %q was rejected but %d bytes were already written", source, buf.Len())
		}
	}
}
