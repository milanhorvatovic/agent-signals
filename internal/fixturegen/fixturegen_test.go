package fixturegen

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"strings"
	"testing"

	"github.com/milanhorvatovic/agent-signals/internal/contract"
)

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
