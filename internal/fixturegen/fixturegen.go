// Package fixturegen deterministically generates the fixture corpora too
// large to commit (fixtures/README.md): the stream that overruns the 16 MiB
// pending-output cap (event-contract.md §Overflow) and duplicate replay
// sets larger than any in-memory dedupe cache (§Spool and cursors). Output
// depends only on the arguments, never on time or randomness, so every
// phase reproduces identical bytes.
package fixturegen

import (
	"fmt"
	"io"
	"strings"

	"github.com/milanhorvatovic/agent-signals/internal/contract"
)

// PendingBytes is the cap on pending, not-yet-appended watcher output the
// supervisor buffers per source. It is an instantaneous byte cap, not a
// time window and not a retention bound — retention is a separate rule
// (event-contract.md §Overflow, §Rotation).
const PendingBytes = 16 << 20

const (
	// Fixed timestamp: generated events must stay deterministic.
	eventTS = "2026-08-24T09:12:03Z"
	// ~1 KiB of padding keeps the line count in the thousands while every
	// line stays a valid bounded event. It rides in data, not summary:
	// summary is capped at 512 scalars and linted against a ~120-character
	// target, while data is unbounded below the per-line byte cap
	// (event-contract.md §Event).
	padRunes = 1024
)

// PendingOverflowStream writes valid single-line events for source until the
// total written size exceeds PendingBytes, then one more, and returns the
// count and total bytes. The event past the crossing is the point of the
// corpus: it is output the supervisor has to drop, where the event that
// merely fills the buffer is not. IDs are source-namespaced sequence
// numbers, so a rerun produces the same IDs (watcher requirement 3).
func PendingOverflowStream(w io.Writer, source string) (events int, total int64, err error) {
	if err := contract.ValidateSlug("source", source); err != nil {
		return 0, 0, err
	}
	pad := strings.Repeat("x", padRunes)
	for total <= PendingBytes {
		n, err := writeEvent(w, source, events, pad)
		if err != nil {
			return events, total, err
		}
		events++
		total += int64(n)
	}
	n, err := writeEvent(w, source, events, pad)
	if err != nil {
		return events, total, err
	}
	return events + 1, total + int64(n), nil
}

// DuplicateReplaySet writes n distinct events followed by the same n events
// again, byte-identical — the replay-larger-than-any-cache input for exact
// retained-history dedupe (§Spool and cursors).
func DuplicateReplaySet(w io.Writer, source string, n int) error {
	if err := contract.ValidateSlug("source", source); err != nil {
		return err
	}
	for round := 0; round < 2; round++ {
		for i := 0; i < n; i++ {
			if _, err := writeEvent(w, source, i, "duplicate replay"); err != nil {
				return err
			}
		}
	}
	return nil
}

func writeEvent(w io.Writer, source string, seq int, padding string) (int, error) {
	return fmt.Fprintf(w,
		`{"id":"%s-%08d","ts":"%s","source":"%s","kind":"generated","severity":"info","summary":"generated event %08d","data":{"padding":"%s"}}`+"\n",
		source, seq, eventTS, source, seq, padding)
}
