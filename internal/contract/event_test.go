package contract

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

func readFixture(t *testing.T, parts ...string) []byte {
	t.Helper()
	path := filepath.Join(append([]string{"..", "..", "fixtures"}, parts...)...)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func globFixtures(t *testing.T, parts ...string) []string {
	t.Helper()
	pattern := filepath.Join(append([]string{"..", "..", "fixtures"}, parts...)...)
	matches, err := filepath.Glob(pattern)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) == 0 {
		t.Fatalf("no fixtures match %s", pattern)
	}
	return matches
}

func TestValidEventFixtures(t *testing.T) {
	for _, path := range globFixtures(t, "events", "valid", "*.jsonl") {
		t.Run(filepath.Base(path), func(t *testing.T) {
			line, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			event, warnings, err := ParseEventLine(line, 0, WatcherInput)
			if err != nil {
				t.Fatalf("valid fixture rejected: %v", err)
			}
			wantWarning := strings.HasPrefix(filepath.Base(path), "long-summary")
			if gotWarning := len(warnings) > 0; gotWarning != wantWarning {
				t.Fatalf("warnings = %v, want warning present = %v", warnings, wantWarning)
			}
			if event.ID == "" || event.TS.IsZero() {
				t.Fatalf("typed event not populated: %+v", event)
			}
		})
	}
}

// TestInvalidEventFixtures holds each negative fixture to the layer that
// rejects it. "Rejected somehow" is too weak a claim here: the corpus guard
// already pins every schema-level fixture to its clause, so what is left for
// this side is which fixtures the parse profile turns away before the schema
// ever runs — the property that would silently disappear if a pre-decode
// check were dropped and the schema happened to catch the same input.
func TestInvalidEventFixtures(t *testing.T) {
	profileOwned := map[string]bool{
		// A top-level array decodes cleanly and is refused for not being an
		// object; the schema would also refuse it, which is exactly why the
		// layer has to be asserted rather than the outcome.
		"array-top-level.jsonl": true,
		// A repeated member name never reaches the schema: a permissive
		// decoder keeps the last one, so the profile rejects it at decode.
		"duplicate-keys.jsonl": true,
		// The reserved prefix decides which schema an event answers to, so it
		// is read before validation rather than expressed as a clause in one.
		"reserved-synthetic-id.jsonl": true,
	}
	for _, path := range globFixtures(t, "events", "invalid", "*.jsonl") {
		t.Run(filepath.Base(path), func(t *testing.T) {
			line, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			_, _, err = ParseEventLine(line, 0, WatcherInput)
			if err == nil {
				t.Fatal("invalid fixture accepted")
			}
			var schemaErr *jsonschema.ValidationError
			if bySchema := errors.As(err, &schemaErr); bySchema == profileOwned[filepath.Base(path)] {
				layer := "the parse profile"
				if bySchema {
					layer = "schema validation"
				}
				t.Fatalf("rejected by %s: %v", layer, err)
			}
		})
	}
}

func TestSyntheticEventFixtures(t *testing.T) {
	for _, path := range globFixtures(t, "synthetic", "*.jsonl") {
		t.Run(filepath.Base(path), func(t *testing.T) {
			line, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := ParseEventLine(line, 0, DeliveryRecord); err != nil {
				t.Fatalf("synthetic fixture rejected from delivery output: %v", err)
			}
			if _, _, err := ParseEventLine(line, 0, WatcherInput); err == nil {
				t.Fatal("reserved-prefix event accepted as watcher input")
			}
		})
	}
}

// TestUnknownSyntheticKindIsRejected covers the reserved prefix with no
// record kind behind it. Both known kinds are held to their own subschema, so
// without this an unrecognized one would fall through to whichever branch was
// written last rather than being refused.
func TestUnknownSyntheticKindIsRejected(t *testing.T) {
	line := []byte(`{"id":"` + SyntheticIDPrefix + `heartbeat:1","ts":"2026-08-24T09:12:03Z",` +
		`"source":"pr-comments","kind":"heartbeat","severity":"info","summary":"not a contract record"}`)
	for _, profile := range []Profile{WatcherInput, SpoolRecord, DeliveryRecord} {
		if _, _, err := ParseEventLine(line, 0, profile); err == nil {
			t.Errorf("profile %d accepted an unknown synthetic kind", profile)
		}
	}
}

// TestProjectionParsesOnlyInDelivery covers the reserved marker delivery
// substitutes for an oversized event's data. It is a delivery representation
// and is never appended anywhere, so the profile that admits it is the only
// one that may: routing delivery records through the watcher schema would
// make a conforming projection unparseable.
func TestProjectionParsesOnlyInDelivery(t *testing.T) {
	line := eventLine(`"summary":"an oversized event","data":{"$projected":true}`)
	if _, _, err := ParseEventLine(line, 0, DeliveryRecord); err != nil {
		t.Fatalf("projection rejected from delivery output: %v", err)
	}
	for name, profile := range map[string]Profile{"watcher input": WatcherInput, "spool": SpoolRecord} {
		if _, _, err := ParseEventLine(line, 0, profile); err == nil {
			t.Errorf("projection accepted as %s", name)
		}
	}
	// The marker is reserved by its exact value, so an ordinary payload that
	// merely mentions it stays legal everywhere — otherwise this test would
	// pass just as well against a profile that rejected all data.
	ordinary := eventLine(`"summary":"ordinary","data":{"$projected":true,"extra":1}`)
	for name, profile := range map[string]Profile{"watcher input": WatcherInput, "delivery": DeliveryRecord} {
		if _, _, err := ParseEventLine(ordinary, 0, profile); err != nil {
			t.Errorf("payload that is not the marker rejected as %s: %v", name, err)
		}
	}
}

// TestUnknownProfileFailsClosed pins the enum as a trust boundary. Falling
// through would hand an ordinary event the watcher schema and admit an
// overflow record, so an out-of-range value would read as a valid
// non-watcher stream rather than as the programming error it is.
func TestUnknownProfileFailsClosed(t *testing.T) {
	unknown := DeliveryRecord + 1
	for name, line := range map[string][]byte{
		"ordinary": eventLine(`"summary":"ordinary"`),
		"overflow": readFixture(t, "synthetic", "overflow-single.jsonl"),
	} {
		if _, _, err := ParseEventLine(line, 0, unknown); err == nil {
			t.Errorf("%s record accepted under an unknown profile", name)
		}
	}
}

func TestParseTimestamp(t *testing.T) {
	const valid = "2026-08-24T09:12:03Z"
	if _, err := parseTimestamp(valid); err != nil {
		t.Fatalf("%s rejected: %v", valid, err)
	}
	// Each of these is refused by the schema pattern first in a full parse.
	// They are asserted here against the timestamp rule itself so this layer
	// cannot quietly come to depend on validation order.
	for _, raw := range []string{
		"2026-02-30T09:12:03Z", // inside every field range, outside the calendar
		"2026-08-24T09:12:03.5Z",
		"2026-08-24T09:12:03+00:00",
		"2026-08-24T09:12:03",
		"2026-08-24 09:12:03Z",
	} {
		if _, err := parseTimestamp(raw); err == nil {
			t.Errorf("%s accepted", raw)
		}
	}
}

// TestRejectsUnpairedSurrogates covers the parse-profile clause the schema
// documents as out of its reach. Go's decoder turns every surrogate half into
// U+FFFD, so the check has to happen before decoding — after it, the input is
// indistinguishable from a line that carried a replacement character.
func TestRejectsUnpairedSurrogates(t *testing.T) {
	for name, summary := range map[string]string{
		"high alone":          `\ud83d`,
		"low alone":           `\ude00`,
		"high then non-pair":  `\ud83dA`,
		"high at end":         `x\ud83d`,
		"high then high":      `\ud83d\ud83d`,
		"inside a nested key": `ok`,
	} {
		t.Run(name, func(t *testing.T) {
			line := eventLine(`"summary":"` + summary + `"`)
			if name == "inside a nested key" {
				line = eventLine(`"summary":"ok","data":{"\udfff":1}`)
			}
			if _, _, err := ParseEventLine(line, 0, WatcherInput); err == nil {
				t.Fatal("accepted a string carrying an unpaired surrogate")
			}
		})
	}
	// The paired form is the control: a valid pair must still be accepted, or
	// the check above would pass by rejecting every escape.
	if _, _, err := ParseEventLine(eventLine(`"summary":"😀"`), 0, WatcherInput); err != nil {
		t.Fatalf("valid surrogate pair rejected: %v", err)
	}
	// An escaped backslash does not open an escape, so this is the literal
	// text \ud800 and carries no surrogate at all.
	if _, _, err := ParseEventLine(eventLine(`"summary":"\\ud800"`), 0, WatcherInput); err != nil {
		t.Fatalf("escaped backslash treated as a surrogate escape: %v", err)
	}
}

func TestRejectsInvalidUTF8(t *testing.T) {
	line := eventLine(`"summary":"ok"`)
	// A raw surrogate half is one of the byte sequences UTF-8 excludes, and
	// the decoder would replace it silently.
	broken := bytes.Replace(line, []byte(`"ok"`), []byte("\"\xed\xa0\x80\""), 1)
	if _, _, err := ParseEventLine(broken, 0, WatcherInput); err == nil {
		t.Fatal("accepted a raw surrogate encoding")
	}
}

func TestNestingDepthCap(t *testing.T) {
	// §Event states the cap as a number, so the number is written here rather
	// than read from MaxDepth: inputs derived from the constant would move
	// with it, and the pair would keep straddling whatever cap the code had.
	const contractDepth = 64
	if MaxDepth != contractDepth {
		t.Fatalf("MaxDepth is %d; the contract caps nesting at %d", MaxDepth, contractDepth)
	}
	// The root object is depth 1, so a data value nesting contractDepth-1
	// objects puts the document exactly on the cap.
	if _, _, err := ParseEventLine(eventLineNesting(contractDepth-1), 0, WatcherInput); err != nil {
		t.Fatalf("document at the %d-level cap rejected: %v", contractDepth, err)
	}
	if _, _, err := ParseEventLine(eventLineNesting(contractDepth), 0, WatcherInput); err == nil {
		t.Fatalf("document one level past the %d-level cap accepted", contractDepth)
	}
}

// eventLine builds a minimal valid event, with fields overriding the
// defaults for the ones it names.
func eventLine(fields string) []byte {
	defaults := map[string]string{
		"id":       `"evt-1"`,
		"ts":       `"2026-08-24T09:12:03Z"`,
		"source":   `"pr-comments"`,
		"kind":     `"review_comment"`,
		"severity": `"info"`,
		"summary":  `"a summary"`,
	}
	var members []string
	for _, name := range []string{"id", "ts", "source", "kind", "severity", "summary"} {
		if !strings.Contains(fields, `"`+name+`":`) {
			members = append(members, `"`+name+`":`+defaults[name])
		}
	}
	members = append(members, fields)
	return []byte("{" + strings.Join(members, ",") + "}")
}

// eventLineNesting returns an event whose data nests count objects, so the
// whole document is count+1 levels deep counting the root.
func eventLineNesting(count int) []byte {
	nested := "{}"
	for range count - 1 {
		nested = `{"n":` + nested + `}`
	}
	return eventLine(`"summary":"deep","data":` + nested)
}

func TestCanonicalSerializationFixture(t *testing.T) {
	a := parse(t, readFixture(t, "events", "canonical", "same-a.jsonl"))
	b := parse(t, readFixture(t, "events", "canonical", "same-b.jsonl"))
	if a.CanonicalDigest != b.CanonicalDigest {
		t.Fatal("reordered/whitespace-shifted content must digest identically")
	}

	value, err := decodeStrict(bytes.TrimSuffix(readFixture(t, "events", "canonical", "same-a.jsonl"), []byte("\n")))
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := appendCanonical(nil, value)
	if err != nil {
		t.Fatal(err)
	}
	if want := readFixture(t, "events", "canonical", "same.canonical"); !bytes.Equal(canonical, want) {
		t.Fatalf("canonical bytes drifted:\n got %s\nwant %s", canonical, want)
	}
	sum := sha256.Sum256(canonical)
	if want := strings.TrimSpace(string(readFixture(t, "events", "canonical", "same.sha256"))); hex.EncodeToString(sum[:]) != want {
		t.Fatalf("canonical digest drifted: got %x want %s", sum, want)
	}
}

// TestCanonicalKeyOrderIsUTF16 pins the one ordering rule Go's native string
// comparison gets wrong. The pair is chosen so the two orders disagree: a
// supplementary character sorts below U+E000 as UTF-16 code units and above
// it as UTF-8 bytes, so a byte-ordered implementation fails this and passes
// on any all-BMP input.
func TestCanonicalKeyOrderIsUTF16(t *testing.T) {
	const (
		supplementary = "\U00010000"
		bmp           = "\uE000"
	)
	if supplementary < bmp {
		t.Fatal("the key pair no longer separates the two orderings; Go's byte order already agrees")
	}
	value, err := decodeStrict([]byte(`{"` + bmp + `":1,"` + supplementary + `":2}`))
	if err != nil {
		t.Fatal(err)
	}
	got, err := appendCanonical(nil, value)
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"` + supplementary + `":2,"` + bmp + `":1}`; string(got) != want {
		t.Errorf("canonical form is %q, want %q", got, want)
	}
}

// TestCanonicalEscapes pins the escape table, including the extension the
// contract adds to RFC 8785 and the three characters encoding/json would
// escape on its own — HTML escaping is off, so <, > and & stay raw UTF-8.
func TestCanonicalEscapes(t *testing.T) {
	in := "\u0085\u2028\u2029<>&\x00\b\t\n\f\r\"\\\u007f"
	want := `"` + `\u0085\u2028\u2029` + `<>&` + `\u0000\b\t\n\f\r` + `\"\\` + "\u007f" + `"`
	if got := string(appendCanonicalString(nil, in)); got != want {
		t.Errorf("canonical string is %q, want %q", got, want)
	}
}

func TestNumberLexemesDigestDifferently(t *testing.T) {
	intForm := parse(t, readFixture(t, "events", "canonical", "lexeme-int.jsonl"))
	floatForm := parse(t, readFixture(t, "events", "canonical", "lexeme-float.jsonl"))
	if intForm.CanonicalDigest == floatForm.CanonicalDigest {
		t.Fatal("1 and 1.0 must keep distinct lexemes, so distinct digests")
	}
}

func TestSameIDDifferentContentIsConflict(t *testing.T) {
	a := parse(t, readFixture(t, "events", "canonical", "conflict-a.jsonl"))
	b := parse(t, readFixture(t, "events", "canonical", "conflict-b.jsonl"))
	if a.ID != b.ID {
		t.Fatal("conflict pair must share an ID")
	}
	if a.CanonicalDigest == b.CanonicalDigest {
		t.Fatal("changed content must change the digest — this pair is the hard-conflict case")
	}
}

func TestRawByteBound(t *testing.T) {
	var limits struct {
		MaxEventBytes int `json:"max_event_bytes"`
	}
	if err := json.Unmarshal(readFixture(t, "events", "limits", "limits.json"), &limits); err != nil {
		t.Fatal(err)
	}

	exact := readFixture(t, "events", "limits", "exact-limit.jsonl")
	if len(exact) != limits.MaxEventBytes {
		t.Fatalf("exact-limit fixture is %d bytes, want %d", len(exact), limits.MaxEventBytes)
	}
	if _, _, err := ParseEventLine(exact, limits.MaxEventBytes, WatcherInput); err != nil {
		t.Fatalf("exact-limit line rejected: %v", err)
	}

	over := readFixture(t, "events", "limits", "over-limit.jsonl")
	_, _, err := ParseEventLine(over, limits.MaxEventBytes, WatcherInput)
	if !errors.Is(err, ErrOversized) {
		t.Fatalf("over-limit line: got %v, want ErrOversized before JSON decoding", err)
	}
}

func parse(t *testing.T, line []byte) *Event {
	t.Helper()
	event, _, err := ParseEventLine(line, 0, WatcherInput)
	if err != nil {
		t.Fatal(fmt.Errorf("%s: %w", line, err))
	}
	return event
}
