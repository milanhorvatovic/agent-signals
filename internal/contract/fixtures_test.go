// Corpus tests for the parse profile: every fixture the golden corpus ships
// is driven through this package's parser in the profile it belongs to. The
// corpus carries its own guard in fixtures/corpus_test.go, which holds each
// file to the schema and to the fixtures beside it; what only this side can
// answer is whether the implementation agrees — and, for the profile split,
// which streams a record is admitted into at all.
package contract

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// spoolFixtures are the JSONL files whose records live in a source spool.
// The torn-tail file is excluded: its final fragment is deliberately
// unparseable and has its own test.
var spoolFixtures = []string{
	filepath.Join("spool", "multi-segment", "*.jsonl"),
	filepath.Join("ingest", "*", "events", "*.jsonl"),
}

// records returns the newline-terminated lines of a JSONL fixture.
func records(t *testing.T, path string) [][]byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	cut := bytes.LastIndexByte(raw, '\n')
	if cut != len(raw)-1 {
		t.Fatalf("%s does not end with a newline; only the torn-tail fixture models an unterminated record", path)
	}
	return bytes.Split(raw[:cut], []byte("\n"))
}

func TestSpoolFixturesParseAsSpoolRecords(t *testing.T) {
	for _, pattern := range spoolFixtures {
		for _, path := range globFixtures(t, pattern) {
			for i, line := range records(t, path) {
				if _, _, err := ParseEventLine(line, 0, SpoolRecord); err != nil {
					t.Errorf("%s line %d: %v", path, i+1, err)
				}
			}
		}
	}
}

// TestGapRecordsAreRejectedOutsideDelivery pins the profile split. A gap
// describes one cursor's position, so it is generated at delivery time and
// never appended to the shared spool — the reason each synthetic kind is held
// to its own subschema rather than to their union, which would admit a gap
// anywhere an overflow record is legal.
func TestGapRecordsAreRejectedOutsideDelivery(t *testing.T) {
	for _, path := range globFixtures(t, "synthetic", "gap*.jsonl") {
		t.Run(filepath.Base(path), func(t *testing.T) {
			line := readFixture(t, "synthetic", filepath.Base(path))
			if _, _, err := ParseEventLine(line, 0, DeliveryRecord); err != nil {
				t.Fatalf("gap rejected from delivery output: %v", err)
			}
			for _, profile := range []struct {
				name  string
				value Profile
			}{{"spool", SpoolRecord}, {"watcher input", WatcherInput}} {
				if _, _, err := ParseEventLine(line, 0, profile.value); err == nil {
					t.Errorf("gap accepted as %s", profile.name)
				}
			}
		})
	}
}

// TestOverflowRecordsAreSpoolRecords is the other half of the split: overflow
// records are the service's own, and they are appended to the shared spool.
func TestOverflowRecordsAreSpoolRecords(t *testing.T) {
	for _, path := range globFixtures(t, "synthetic", "overflow-*.jsonl") {
		t.Run(filepath.Base(path), func(t *testing.T) {
			line := readFixture(t, "synthetic", filepath.Base(path))
			for _, profile := range []Profile{SpoolRecord, DeliveryRecord} {
				if _, _, err := ParseEventLine(line, 0, profile); err != nil {
					t.Errorf("overflow record rejected: %v", err)
				}
			}
			if _, _, err := ParseEventLine(line, 0, WatcherInput); err == nil {
				t.Error("reserved-prefix record accepted as watcher input")
			}
		})
	}
}

// TestContextFixturesAreWatcherInput covers the spool files a gap fixture
// derives from. They hold ordinary watcher records, so they must parse under
// the strictest profile rather than merely under the spool one.
func TestContextFixturesAreWatcherInput(t *testing.T) {
	for _, path := range globFixtures(t, "synthetic", "context", "*", "*.jsonl") {
		for i, line := range records(t, path) {
			if _, _, err := ParseEventLine(line, 0, WatcherInput); err != nil {
				t.Errorf("%s line %d: %v", path, i+1, err)
			}
		}
	}
}

// TestTornTailFixture covers adoption note A1: a reader consumes up to the
// last newline, and the trailing partial line is output not yet written.
func TestTornTailFixture(t *testing.T) {
	spool := readFixture(t, "spool", "torn-tail", "pr-comments.jsonl")
	if bytes.HasSuffix(spool, []byte("\n")) {
		t.Fatal("torn-tail fixture must end mid-line")
	}
	cut := bytes.LastIndexByte(spool, '\n')
	complete, torn := spool[:cut+1], spool[cut+1:]
	if len(torn) == 0 {
		t.Fatal("no torn tail present")
	}
	lines := bytes.Split(bytes.TrimSuffix(complete, []byte("\n")), []byte("\n"))
	if len(lines) != 2 {
		t.Fatalf("want 2 complete events before the torn tail, got %d", len(lines))
	}
	for i, line := range lines {
		if _, _, err := ParseEventLine(line, 0, SpoolRecord); err != nil {
			t.Fatalf("complete line %d rejected: %v", i+1, err)
		}
	}
	if _, _, err := ParseEventLine(torn, 0, SpoolRecord); err == nil {
		t.Fatal("torn tail parsed as a complete event")
	}
}

// TestCursorIdentifiersValidateAsSlugs holds ValidateSlug to the values the
// corpus actually ships. The corpus guard checks those values against its own
// copy of the grammar; this is the same claim made of the function the
// service builds paths with, which is where a divergence would matter.
func TestCursorIdentifiersValidateAsSlugs(t *testing.T) {
	for _, path := range globFixtures(t, "cursors", "*", "*", "*", "*.json") {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			Consumer string `json:"consumer"`
			Source   string `json:"source"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		for role, value := range map[string]string{"consumer": doc.Consumer, "source": doc.Source} {
			if err := ValidateSlug(role, value); err != nil {
				t.Errorf("%s: %v", path, err)
			}
		}
	}
}
