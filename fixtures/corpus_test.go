// Package fixtures holds no code — it carries the golden corpus. This test
// is the corpus's own guard: every fixture agrees with the schemas it claims
// to pin, and every value derived by the contract is recomputed from the
// fixture that carries it, so a hand edit cannot silently desynchronize a
// digest from its inputs.
package fixtures

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math/big"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	yaml "go.yaml.in/yaml/v3"

	"github.com/milanhorvatovic/agent-signals/schemas"
)

const (
	eventID     = "https://github.com/milanhorvatovic/agent-signals/schemas/event.schema.json"
	monitorsID  = "https://github.com/milanhorvatovic/agent-signals/schemas/monitors.schema.json"
	syntheticID = eventID + "#/$defs/syntheticEvent"
	reservedPfx = "agent-signals:synthetic:"
	gapIDPrefix = reservedPfx + "gap:"
)

// compiled carries the three schema entry points the corpus is checked against.
type compiled struct {
	watcher   *jsonschema.Schema
	synthetic *jsonschema.Schema
	monitors  *jsonschema.Schema
}

// compile builds the pinned validator with format assertion enabled: `format`
// is annotation-only under the default 2020-12 vocabulary, and the event
// schema's `ts` relies on it for calendar validity.
func compile(t *testing.T) compiled {
	t.Helper()
	c := jsonschema.NewCompiler()
	c.AssertFormat()
	for id, raw := range map[string][]byte{eventID: schemas.Event, monitorsID: schemas.Monitors} {
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
		if err != nil {
			t.Fatalf("unmarshal %s: %v", id, err)
		}
		if err := c.AddResource(id, doc); err != nil {
			t.Fatalf("add %s: %v", id, err)
		}
	}
	var out compiled
	for _, step := range []struct {
		id  string
		dst **jsonschema.Schema
	}{{eventID, &out.watcher}, {syntheticID, &out.synthetic}, {monitorsID, &out.monitors}} {
		sch, err := c.Compile(step.id)
		if err != nil {
			t.Fatalf("compile %s: %v", step.id, err)
		}
		*step.dst = sch
	}
	return out
}

func decodeJSON(t *testing.T, raw []byte) any {
	t.Helper()
	// The decoder keeps the last of a repeated member silently, so without
	// this a fixture the corpus treats as well-formed could carry a duplicate
	// key the parse profile is required to reject — a golden record the
	// strict decoder could not accept. The one fixture that demonstrates the
	// permissive path deliberately does not come through here.
	if containsDuplicateKey(raw) {
		t.Fatalf("repeated member name in %s", raw)
	}
	v, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return v
}

// rejectionLeaves reports the keyword that actually rejected an instance, as
// "<Keyword> at /<instance path>", for every leaf of the validation error.
// The intermediate $ref and allOf nodes are structure, not cause.
func rejectionLeaves(err error) []string {
	var ve *jsonschema.ValidationError
	if !errors.As(err, &ve) {
		return nil
	}
	var out []string
	var walk func(e *jsonschema.ValidationError)
	walk = func(e *jsonschema.ValidationError) {
		if len(e.Causes) == 0 {
			kind := fmt.Sprintf("%T", e.ErrorKind)
			out = append(out, kind[strings.LastIndex(kind, ".")+1:]+" at /"+strings.Join(e.InstanceLocation, "/"))
			return
		}
		for _, cause := range e.Causes {
			walk(cause)
		}
	}
	walk(ve)
	return out
}

// assertRejectedBy requires the named keyword and instance path to be among
// the reasons an instance was rejected. "Rejected at all" is too weak for a
// negative fixture: a severity fixture that lost a required field would still
// be rejected, and would still pass, while no longer testing severity.
func assertRejectedBy(t *testing.T, name string, err error, want string) {
	t.Helper()
	if err == nil {
		t.Errorf("%s: accepted, want reject by %s", name, want)
		return
	}
	got := rejectionLeaves(err)
	if slices.Contains(got, want) {
		return
	}
	t.Errorf("%s: rejected by %v, want %s — the fixture no longer isolates its documented clause", name, got, want)
}

// records returns every newline-terminated line of a JSONL fixture, dropping
// an unterminated final fragment — the torn tail a reader must stop before.
// A fixture with no terminator at all is a framing error rather than an empty
// file: returning nothing there would drop a one-record fixture out of every
// glob-driven check while leaving them green. Blank lines are returned as
// empty records for the same reason, and fail at decode.
func records(t *testing.T, path string) [][]byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	cut := bytes.LastIndexByte(raw, '\n')
	if cut < 0 {
		t.Fatalf("%s carries no newline terminator; a JSONL fixture must end its records", path)
	}
	return bytes.Split(raw[:cut], []byte("\n"))
}

// containsDuplicateKey reports whether any JSON object in raw repeats a member
// name. encoding/json keeps the last occurrence silently, which is exactly why
// the parse profile rejects the input before decoding.
func containsDuplicateKey(raw []byte) bool {
	type frame struct {
		object  bool
		wantKey bool
		keys    map[string]bool
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	var stack []*frame
	for {
		tok, err := dec.Token()
		if err != nil {
			return false
		}
		delim, isDelim := tok.(json.Delim)
		if isDelim && (delim == '}' || delim == ']') {
			stack = stack[:len(stack)-1]
			continue
		}
		if n := len(stack); n > 0 && stack[n-1].object && stack[n-1].wantKey {
			key, ok := tok.(string)
			if !ok {
				return false
			}
			if stack[n-1].keys[key] {
				return true
			}
			stack[n-1].keys[key] = true
			stack[n-1].wantKey = false
			continue
		}
		// This token opens a value, so the enclosing object expects a key next.
		if n := len(stack); n > 0 && stack[n-1].object {
			stack[n-1].wantKey = true
		}
		if isDelim {
			stack = append(stack, &frame{object: delim == '{', wantKey: delim == '{', keys: map[string]bool{}})
		}
	}
}

// decodeWithNumbers decodes a fixture preserving number lexemes, so 1.50 and
// 1.5 stay distinguishable — the corpus pins that distinction.
func decodeWithNumbers(t *testing.T, path string) any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	// Decode stops after one value and ignores whatever follows, so a second
	// record appended to a single-record fixture would pass unnoticed.
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		t.Fatalf("%s carries more than one JSON value", path)
	}
	return v
}

func glob(t *testing.T, pattern string) []string {
	t.Helper()
	m, err := filepath.Glob(pattern)
	if err != nil {
		t.Fatalf("glob %s: %v", pattern, err)
	}
	if len(m) == 0 {
		t.Fatalf("glob %s matched nothing", pattern)
	}
	return m
}

// spoolSource derives the source a spool file belongs to from its name:
// <source>.jsonl for the active file, <source>.<seq>.jsonl for an archive. A
// source may itself contain dots, so only an all-digit final segment is
// treated as the rotation suffix.
func spoolSource(path string) string {
	name := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	if i := strings.LastIndex(name, "."); i >= 0 {
		if _, err := strconv.Atoi(name[i+1:]); err == nil {
			return name[:i]
		}
	}
	return name
}

func recordID(inst any) string {
	obj, _ := inst.(map[string]any)
	id, _ := obj["id"].(string)
	return id
}

// TestWatcherFixturesValidate covers every fixture a watcher could have
// emitted. Synthetic records living in the same spool files are rejected here
// by construction: the reserved prefix is service-owned.
func TestWatcherFixturesValidate(t *testing.T) {
	s := compile(t)
	// perSource marks the patterns whose filenames name the spool they belong
	// to. The standalone event fixtures are named for what they demonstrate,
	// so no such coupling exists for them.
	patterns := []struct {
		glob      string
		perSource bool
	}{
		{"events/valid/*.jsonl", false},
		{"events/canonical/*.jsonl", false},
		{"events/limits/*.jsonl", false},
		{"spool/*/*.jsonl", true},
		{"ingest/*/events/*.jsonl", true},
		{"synthetic/context/*.jsonl", true},
	}
	for _, p := range patterns {
		for _, path := range glob(t, p.glob) {
			for i, line := range records(t, path) {
				inst := decodeJSON(t, line)
				err := s.watcher.Validate(inst)
				synth := strings.HasPrefix(recordID(inst), reservedPfx)
				switch {
				case synth && err == nil:
					t.Errorf("%s line %d: synthetic ID accepted as watcher input", path, i+1)
				case !synth && err != nil:
					t.Errorf("%s line %d: %v", path, i+1, err)
				}
				if !p.perSource {
					continue
				}
				// A spool holds one source. Every record here is schema-valid
				// with any slug in that field, so without this a fixture could
				// model cross-source corruption while claiming to be a spool.
				if source, _ := inst.(map[string]any)["source"].(string); source != spoolSource(path) {
					t.Errorf("%s line %d: record is from %q, the file is the %q spool", path, i+1, source, spoolSource(path))
				}
			}
		}
	}
}

func TestInvalidEventFixturesReject(t *testing.T) {
	s := compile(t)
	// Duplicate object keys are rejected by the parse profile at decode; a
	// JSON decoder collapses them before the schema ever sees the instance, so
	// that fixture is asserted against the decode-level property instead.
	decodeLevel := map[string]bool{"duplicate-keys.jsonl": true}
	// Each fixture names the clause it is for, so each is held to that clause
	// rather than to being rejected somehow.
	wantRejection := map[string]string{
		"array-top-level.jsonl":       "Type at /",
		"bad-calendar-ts.jsonl":       "Format at /ts",
		"empty-source.jsonl":          "Pattern at /source",
		"empty-summary.jsonl":         "MinLength at /summary",
		"missing-id.jsonl":            "Required at /",
		"multiline-summary.jsonl":     "Pattern at /summary",
		"non-utc-ts.jsonl":            "Pattern at /ts",
		"reserved-synthetic-id.jsonl": "Not at /id",
		"uppercase-source.jsonl":      "Pattern at /source",
		"wrong-severity.jsonl":        "Enum at /severity",
	}
	for _, path := range glob(t, "events/invalid/*.jsonl") {
		name := filepath.Base(path)
		if decodeLevel[name] {
			continue
		}
		want, known := wantRejection[name]
		if !known {
			t.Errorf("%s: no documented rejection clause; add one so the fixture cannot pass for an unintended reason", name)
			continue
		}
		assertRejectedBy(t, name, s.watcher.Validate(decodeJSON(t, records(t, path)[0])), want)
	}
}

// TestCalendarValidityNeedsFormatAssertion proves the calendar fixture
// depends on the format assertion rather than merely being rejected. The ts
// pattern already pins each field's range, so a value like month 13 fails on
// the pattern alone and would keep passing with AssertFormat removed — the
// fixture would then guard nothing it claims to guard. February 30 is inside
// every field range and outside the calendar, so only the format assertion
// can reject it.
func TestCalendarValidityNeedsFormatAssertion(t *testing.T) {
	c := jsonschema.NewCompiler()
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schemas.Event))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.AddResource(eventID, doc); err != nil {
		t.Fatal(err)
	}
	lenient, err := c.Compile(eventID)
	if err != nil {
		t.Fatal(err)
	}
	inst := decodeJSON(t, records(t, "events/invalid/bad-calendar-ts.jsonl")[0])
	if err := lenient.Validate(inst); err != nil {
		t.Errorf("rejected without format assertion (%v); the fixture must isolate calendar validity, which the pattern cannot express", err)
	}
	if err := compile(t).watcher.Validate(inst); err == nil {
		t.Error("accepted with format assertion enabled")
	}
}

// TestLongSummaryCrossesTheLintBoundary pins the one fixture whose
// expectation is deliberately outside the schema: §Why `summary` is
// constrained sets a ~120-character target enforced as a lint warning, so
// nothing in the schema would notice this fixture being shortened under it.
func TestLongSummaryCrossesTheLintBoundary(t *testing.T) {
	const target = 120
	rec := decodeJSON(t, records(t, "events/valid/long-summary.jsonl")[0])
	summary, _ := rec.(map[string]any)["summary"].(string)
	if n := len([]rune(summary)); n <= target {
		t.Errorf("summary is %d scalars; this fixture exists to sit above the ~%d-character lint target", n, target)
	}
}

// TestDuplicateKeyFixtureRepeatsAKey asserts the property its fixture exists
// for. Without this the exemption above would leave the file unchecked, and
// rewriting it into ordinary valid JSON would keep the corpus green while the
// stated decode-level expectation quietly described nothing.
func TestDuplicateKeyFixtureRepeatsAKey(t *testing.T) {
	line := records(t, "events/invalid/duplicate-keys.jsonl")[0]
	if !containsDuplicateKey(line) {
		t.Error("duplicate-keys.jsonl carries no repeated member name")
	}
	// The permissive path is the reason the parse profile owns this rejection.
	if _, err := jsonschema.UnmarshalJSON(bytes.NewReader(line)); err != nil {
		t.Errorf("a permissive decoder should accept it; the parse profile is what must reject it: %v", err)
	}
}

func TestSyntheticFixturesValidate(t *testing.T) {
	s := compile(t)
	paths := append(glob(t, "synthetic/*.jsonl"), "ingest/synthetic-tail/events/pr-comments.jsonl")
	seen := 0
	for _, path := range paths {
		for i, line := range records(t, path) {
			inst := decodeJSON(t, line)
			if !strings.HasPrefix(recordID(inst), reservedPfx) {
				// The spooled tail legitimately mixes watcher records with a
				// synthetic one; a file under synthetic/ does not, and a
				// watcher record there would be checked against nothing.
				if strings.HasPrefix(path, "synthetic/") {
					t.Errorf("%s line %d: %q is not a synthetic record", path, i+1, recordID(inst))
				}
				continue
			}
			seen++
			if err := s.synthetic.Validate(inst); err != nil {
				t.Errorf("%s line %d: %v", path, i+1, err)
			}
		}
	}
	if seen != len(glob(t, "synthetic/*.jsonl"))+1 {
		t.Errorf("checked %d synthetic records, want one per fixture plus the spooled overflow tail", seen)
	}
}

// contextEvent finds the watcher record a synthetic fixture derives from.
// synthetic/context holds the events a gap refers to but does not contain,
// so the derived fields can be checked against their real inputs.
func contextEvent(t *testing.T, source, id string) (map[string]any, bool) {
	t.Helper()
	for _, line := range records(t, filepath.Join("synthetic/context", source+".jsonl")) {
		rec, _ := decodeJSON(t, line).(map[string]any)
		if rec["id"] == id {
			return rec, true
		}
	}
	return nil, false
}

// renderInterpolatedID applies §Rotation's truncation rule for an ID placed
// in a synthetic summary: past 120 scalars it becomes its first 96, an
// ellipsis, and 16 hex characters of its digest, so a summary is bounded
// however long its inputs run. No fixture reaches the bound today; the rule
// is implemented rather than assumed so one could be added without the
// comparison quietly becoming wrong.
func renderInterpolatedID(id string) string {
	scalars := []rune(id)
	if len(scalars) <= 120 {
		return id
	}
	digest := sha256.Sum256([]byte(id))
	return string(scalars[:96]) + "…" + hex.EncodeToString(digest[:])[:16]
}

// TestMultiSegmentOrdering pins the rotation layout itself: archives in name
// order carry strictly earlier events than the archives after them, and the
// active file carries the newest. Schema-validating each record says nothing
// about the arrangement, which is the whole point of the fixture. Ordering is
// read from the fixture's own pr-<n> ID sequence.
func TestMultiSegmentOrdering(t *testing.T) {
	archives := glob(t, "spool/multi-segment/*.[0-9]*.jsonl")
	sort.Strings(archives)
	ordered := append(archives, "spool/multi-segment/pr-comments.jsonl")
	var prev int64
	prevName := ""
	for _, path := range ordered {
		for i, line := range records(t, path) {
			id := recordID(decodeJSON(t, line))
			n, err := strconv.ParseInt(id[strings.LastIndex(id, "-")+1:], 10, 64)
			if err != nil {
				t.Fatalf("%s line %d: ID %q does not end in the fixture's decimal sequence", path, i+1, id)
			}
			if n <= prev {
				t.Errorf("%s carries %q after %q in %s; segments run oldest to newest, active file last",
					path, id, "pr-"+strconv.FormatInt(prev, 10), prevName)
			}
			prev, prevName = n, path
		}
	}
}

// TestOverflowSummariesRenderFromData rebuilds each overflow summary from the
// record's own reason and count. The schema pins the template and the
// singular/plural split, but not the number itself — it cannot compare two
// sibling members — so "dropped 2 events" against a dropped_count of 3 would
// validate cleanly while contradicting the record it describes.
func TestOverflowSummariesRenderFromData(t *testing.T) {
	paths := append(glob(t, "synthetic/overflow-*.jsonl"), "ingest/synthetic-tail/events/pr-comments.jsonl")
	seen := 0
	for _, path := range paths {
		for i, line := range records(t, path) {
			var rec struct {
				ID      string `json:"id"`
				Source  string `json:"source"`
				Summary string `json:"summary"`
				Data    struct {
					Reason       string `json:"reason"`
					DroppedCount int    `json:"dropped_count"`
				} `json:"data"`
			}
			if err := json.Unmarshal(line, &rec); err != nil {
				t.Fatalf("%s line %d: %v", path, i+1, err)
			}
			prefix := reservedPfx + "overflow:"
			if !strings.HasPrefix(rec.ID, prefix) {
				continue
			}
			seen++
			// The ID embeds the source it was allocated for. The schema pins
			// the grammar of that component but cannot compare it against the
			// record's own source, so a valid slug from another source would
			// pass every other guard while naming a counter no implementation
			// would have advanced for this one.
			rest := rec.ID[len(prefix):]
			if embedded := rest[:strings.LastIndex(rest, ":")]; embedded != rec.Source {
				t.Errorf("%s line %d: ID names source %q, record is from %q", path, i+1, embedded, rec.Source)
			}
			var want string
			switch rec.Data.Reason {
			case "pending_bytes_exceeded":
				noun := "events"
				if rec.Data.DroppedCount == 1 {
					noun = "event"
				}
				want = fmt.Sprintf("dropped %d %s: pending byte cap exceeded", rec.Data.DroppedCount, noun)
			case "line_limit_exceeded":
				want = "dropped an unparseable oversized line"
			case "malformed_line":
				want = "dropped an unparseable malformed line"
			default:
				t.Errorf("%s line %d: reason %q is outside the closed vocabulary", path, i+1, rec.Data.Reason)
				continue
			}
			if rec.Summary != want {
				t.Errorf("%s line %d:\n  summary %q\n  renders %q from reason %q and count %d",
					path, i+1, rec.Summary, want, rec.Data.Reason, rec.Data.DroppedCount)
			}
		}
	}
	if seen != len(glob(t, "synthetic/overflow-*.jsonl"))+1 {
		t.Errorf("checked %d overflow records, want one per fixture plus the spooled tail", seen)
	}
}

// TestGapIDsRecompute derives each gap fixture's ID from the fixture's own
// data and rejects a mismatch: the ID is the SHA-256 of a canonical JSON
// array over the same components, so editing one without the other would
// publish a gap that no implementation can reproduce.
func TestGapIDsRecompute(t *testing.T) {
	for _, path := range glob(t, "synthetic/gap*.jsonl") {
		var rec struct {
			ID      string `json:"id"`
			TS      string `json:"ts"`
			Source  string `json:"source"`
			Summary string `json:"summary"`
			Data    struct {
				CursorID         string  `json:"cursor_id"`
				LastID           *string `json:"last_id"`
				LastRemovedID    string  `json:"last_removed_id"`
				FirstAvailableID string  `json:"first_available_id"`
			} `json:"data"`
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &rec); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		// cursor_id is <consumer>/<instance>/<source>; <safe-instance> is the
		// lowercase hex SHA-256 of the instance's UTF-8 bytes. Consumer and
		// source are slugs that cannot contain a slash, but instance is an
		// opaque string that can, so it is everything between them rather than
		// the second of three fields.
		head := strings.Index(rec.Data.CursorID, "/")
		tail := strings.LastIndex(rec.Data.CursorID, "/")
		if head < 0 || head == tail {
			t.Fatalf("%s: cursor_id %q is not <consumer>/<instance>/<source>", path, rec.Data.CursorID)
		}
		consumer, source := rec.Data.CursorID[:head], rec.Data.CursorID[tail+1:]
		// The digest is derived from the cursor's source, so a record whose
		// own source disagrees would carry an ID belonging to a different
		// stream — reproducible, and wrong for the event it is attached to.
		if source != rec.Source {
			t.Errorf("%s: cursor_id names source %q, record is from %q", path, source, rec.Source)
		}
		instance := sha256.Sum256([]byte(rec.Data.CursorID[head+1 : tail]))
		// The digest deliberately excludes last_id and the summary, so both
		// are unpinned by the ID check above: the summary is re-rendered from
		// data here, since §Rotation states it exactly.
		last := "-"
		if rec.Data.LastID != nil {
			last = renderInterpolatedID(*rec.Data.LastID)
		}
		wantSummary := fmt.Sprintf("events after %s were removed by retention; none retained", last)
		if rec.Data.FirstAvailableID != "" {
			wantSummary = fmt.Sprintf("events after %s were removed by retention; resuming from %s",
				last, renderInterpolatedID(rec.Data.FirstAvailableID))
			// ts is the first available event's timestamp, so the retained
			// variant is only checkable against that event. Without the
			// companion record any schema-valid ts would pass.
			first, found := contextEvent(t, rec.Source, rec.Data.FirstAvailableID)
			if !found {
				t.Errorf("%s: no companion record for first_available_id %q; ts cannot be verified", path, rec.Data.FirstAvailableID)
			} else if ts, _ := first["ts"].(string); ts != rec.TS {
				t.Errorf("%s: ts is %q, the first available event %q is at %q", path, rec.TS, rec.Data.FirstAvailableID, ts)
			}
		}
		if rec.Summary != wantSummary {
			t.Errorf("%s:\n  summary %q\n  renders %q from its data", path, rec.Summary, wantSummary)
		}
		var derivation []string
		if rec.Data.FirstAvailableID != "" {
			derivation = []string{"gap", "retained", consumer, hex.EncodeToString(instance[:]), source, rec.Data.FirstAvailableID}
		} else {
			// Empty-source variant: the record derives from the retention
			// tombstone, and ts is the removal time.
			derivation = []string{"gap", "empty", consumer, hex.EncodeToString(instance[:]), source, rec.Data.LastRemovedID, rec.TS}
		}
		// Every element is a control-free ASCII string, so canonical
		// serialization is Go's escape-free array encoding.
		canonical, err := json.Marshal(derivation)
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(canonical)
		want := gapIDPrefix + hex.EncodeToString(digest[:])
		if rec.ID != want {
			t.Errorf("%s:\n  id   %s\n  want %s\n  from %s", path, rec.ID, want, canonical)
		}
	}
}

// TestCanonicalDigestMatches pins same.sha256 to the committed canonical
// bytes, which carry no trailing newline: the digest covers the record, not
// the file's line terminator.
func TestCanonicalDigestMatches(t *testing.T) {
	canonical, err := os.ReadFile("events/canonical/same.canonical")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.HasSuffix(canonical, []byte("\n")) {
		t.Fatal("same.canonical carries a trailing newline; the digest must cover the record alone")
	}
	committed, err := os.ReadFile("events/canonical/same.sha256")
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(canonical)
	if got, want := strings.TrimSpace(string(committed)), hex.EncodeToString(digest[:]); got != want {
		t.Errorf("same.sha256 is %s, canonical bytes digest to %s", got, want)
	}
}

// TestCanonicalInputsAgree ties both inputs to the committed canonical bytes.
// Proving the byte-exact serialization — recursive UTF-16 key ordering, the
// RFC 8785 escape table, preserved number lexemes — needs the canonicalizer
// itself and belongs to the parse profile; what the corpus can prove without
// it is that neither input drifted to different content than same.canonical
// describes, which is the drift a hand edit actually causes.
func TestCanonicalInputsAgree(t *testing.T) {
	want := decodeWithNumbers(t, "events/canonical/same.canonical")
	for _, name := range []string{"same-a.jsonl", "same-b.jsonl"} {
		got := decodeWithNumbers(t, filepath.Join("events/canonical", name))
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s decodes to different content than same.canonical:\n  got  %#v\n  want %#v", name, got, want)
		}
	}
}

// TestNumberLexemesDiffer pins the pair the canonicalization rules exist for:
// 1 and 1.0 are the same value and different lexemes, so they must not
// collapse into one another. The pair only demonstrates that if data.n is the
// single difference between the two records — any other differing field would
// separate their digests on its own and prove nothing about lexemes.
func TestNumberLexemesDiffer(t *testing.T) {
	i := decodeWithNumbers(t, "events/canonical/lexeme-int.jsonl").(map[string]any)
	f := decodeWithNumbers(t, "events/canonical/lexeme-float.jsonl").(map[string]any)
	get := func(v map[string]any) string {
		n, _ := v["data"].(map[string]any)["n"].(json.Number)
		return n.String()
	}
	// Two halves, and the pair needs both: the lexemes must differ, and they
	// must still denote one value. Checking only the first would accept 1
	// against 2.0, which demonstrates nothing about lexeme preservation.
	if get(i) == get(f) {
		t.Errorf("both lexeme fixtures carry n as %q; the pair must stay distinguishable", get(i))
	}
	ri, iOK := new(big.Rat).SetString(get(i))
	rf, fOK := new(big.Rat).SetString(get(f))
	if !iOK || !fOK {
		t.Fatalf("n is not a number in one of the pair: %q, %q", get(i), get(f))
	}
	if ri.Cmp(rf) != 0 {
		t.Errorf("the lexeme pair carries different values (%q and %q); it must be one value in two lexemes", get(i), get(f))
	}
	for field := range i {
		if field == "data" {
			continue
		}
		if !reflect.DeepEqual(i[field], f[field]) {
			t.Errorf("the lexeme pair differs at %q as well as data.n; only the lexeme may vary", field)
		}
	}
	if len(i) != len(f) {
		t.Errorf("the lexeme pair carries different field sets: %d and %d", len(i), len(f))
	}
	for name, rec := range map[string]map[string]any{"lexeme-int": i, "lexeme-float": f} {
		for field := range rec["data"].(map[string]any) {
			if field != "n" {
				t.Errorf("%s: data carries %q as well as n; only the lexeme may vary", name, field)
			}
		}
	}
}

// TestConflictFixturesShareAnID pins the hard-conflict scenario: one ID
// reused for different content, which the spool must never silently drop.
// Schema validation alone would stay green if either fixture drifted off it.
func TestConflictFixturesShareAnID(t *testing.T) {
	a := decodeWithNumbers(t, "events/canonical/conflict-a.jsonl").(map[string]any)
	b := decodeWithNumbers(t, "events/canonical/conflict-b.jsonl").(map[string]any)
	if a["id"] != b["id"] {
		t.Errorf("conflict fixtures carry different IDs (%v, %v); the pair is a duplicate check, not a conflict", a["id"], b["id"])
	}
	if reflect.DeepEqual(a, b) {
		t.Error("conflict fixtures carry identical content; the pair is an accepted duplicate, not a hard conflict")
	}
}

func TestTornTailCarriesAFragment(t *testing.T) {
	raw, err := os.ReadFile("spool/torn-tail/pr-comments.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	cut := bytes.LastIndexByte(raw, '\n')
	if cut == len(raw)-1 {
		t.Fatal("torn-tail ends with a newline; the fixture must carry a torn final fragment")
	}
	if _, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw[cut+1:])); err == nil {
		t.Error("torn fragment decoded as complete JSON")
	}
}

// yamlInstance decodes a manifest fixture through JSON so the validator sees
// the same value types a JSON instance would produce.
func yamlInstance(path string) (any, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	buf, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	return jsonschema.UnmarshalJSON(bytes.NewReader(buf))
}

func TestManifestFixtures(t *testing.T) {
	s := compile(t)
	for _, path := range glob(t, "manifest/valid/*.yaml") {
		inst, err := yamlInstance(path)
		if err != nil {
			t.Errorf("%s: %v", filepath.Base(path), err)
			continue
		}
		if err := s.monitors.Validate(inst); err != nil {
			t.Errorf("%s: %v", filepath.Base(path), err)
		}
	}
	// Cross-entry name uniqueness needs the whole document at once and is the
	// manifest validator's job, not the schema's. Only duplicate-source.yaml
	// is exempt: case-folded-alias.yaml carries an uppercase name, which the
	// schema's lowercase slug pattern rejects on its own.
	validatorSide := map[string]bool{"duplicate-source.yaml": true}
	// Exactly one fixture is rejected before the schema sees it. Treating any
	// decode failure as that rejection would let a schema-level fixture rot
	// into malformed YAML and still pass, and would equally let the
	// duplicate-key fixture start decoding cleanly without anyone noticing.
	decodeLevel := "duplicate-yaml-keys.yaml"
	wantRejection := map[string]string{
		"below-interval-floor.yaml":      "Minimum at /0/interval",
		"case-folded-alias.yaml":         "Pattern at /1/name",
		"not-an-array.yaml":              "Type at /",
		"oversized-max-event-bytes.yaml": "Maximum at /0/max_event_bytes",
		"traversal-name.yaml":            "Pattern at /0/name",
		"unknown-tier.yaml":              "Enum at /0/tiers/1",
	}
	for _, path := range glob(t, "manifest/invalid/*.yaml") {
		name := filepath.Base(path)
		inst, err := yamlInstance(path)
		if name == decodeLevel {
			if err == nil {
				t.Errorf("%s: decoded cleanly; it must be rejected at YAML decode", name)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: failed to decode (%v); only %s is rejected before the schema", name, err, decodeLevel)
			continue
		}
		err = s.monitors.Validate(inst)
		if validatorSide[name] {
			// This one is rejected by cross-entry validation alone, so it must
			// still be schema-valid: a missing required field or an
			// out-of-range value here would pass for the wrong reason.
			if err != nil {
				t.Errorf("%s: %v — it must be schema-valid and rejected only by the validator", name, err)
			}
			continue
		}
		want, known := wantRejection[name]
		if !known {
			t.Errorf("%s: no documented rejection clause; add one so the fixture cannot pass for an unintended reason", name)
			continue
		}
		assertRejectedBy(t, name, err, want)
	}
}

// TestEveryOptionalManifestFieldIsExercised keeps the valid corpus honest
// about its own coverage: a schema change to an optional property that no
// fixture sets would otherwise land with nothing to catch it.
func TestEveryOptionalManifestFieldIsExercised(t *testing.T) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schemas.Monitors))
	if err != nil {
		t.Fatal(err)
	}
	monitor := doc.(map[string]any)["$defs"].(map[string]any)["monitor"].(map[string]any)
	required := map[string]bool{}
	for _, r := range monitor["required"].([]any) {
		required[r.(string)] = true
	}
	// all-options is the fixture that carries the guarantee, so it is held to
	// it directly. Counting appearances across the whole valid corpus would
	// let a field drift into another fixture, or back to its default, while
	// the count stayed the same.
	inst, err := yamlInstance("manifest/valid/all-options.yaml")
	if err != nil {
		t.Fatal(err)
	}
	entries, _ := inst.([]any)
	if len(entries) != 1 {
		t.Fatalf("all-options.yaml carries %d entries, want exactly one", len(entries))
	}
	entry := entries[0].(map[string]any)
	for field, spec := range monitor["properties"].(map[string]any) {
		if required[field] {
			continue
		}
		value, present := entry[field]
		if !present {
			t.Errorf("all-options.yaml does not set the optional field %q", field)
			continue
		}
		// retention_age is unset by default and declares none, so presence is
		// all there is to check for it.
		def, hasDefault := spec.(map[string]any)["default"]
		if hasDefault && fmt.Sprint(value) == fmt.Sprint(def) {
			t.Errorf("all-options.yaml sets %q to its default %v; the fixture exists to exercise non-default values", field, def)
		}
	}
}

// TestDuplicateSourceFixtureCollides asserts the property the one validator-
// side exemption above rests on. The schema accepts this document by design,
// so without this check the fixture would pass the suite while claiming to
// carry a collision it no longer had.
func TestDuplicateSourceFixtureCollides(t *testing.T) {
	inst, err := yamlInstance("manifest/invalid/duplicate-source.yaml")
	if err != nil {
		t.Fatal(err)
	}
	entries, ok := inst.([]any)
	if !ok || len(entries) < 2 {
		t.Fatalf("expected a manifest array of at least two entries, got %T", inst)
	}
	seen := map[string]bool{}
	for _, e := range entries {
		name, _ := e.(map[string]any)["name"].(string)
		folded := strings.ToLower(name)
		if seen[folded] {
			return
		}
		seen[folded] = true
	}
	t.Error("duplicate-source.yaml carries no colliding name; the validator-side exemption covers nothing")
}

// TestLimitFixtureBounds pins the raw byte bound the parse profile enforces
// before JSON decoding, newline included.
func TestLimitFixtureBounds(t *testing.T) {
	var limits struct {
		MaxEventBytes int64 `json:"max_event_bytes"`
	}
	raw, err := os.ReadFile("events/limits/limits.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &limits); err != nil {
		t.Fatal(err)
	}
	// The boundary has to be one a source could actually configure, so it is
	// pinned to the schema's own floor rather than an arbitrary small number:
	// below it, both fixtures would model an unreachable configuration.
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schemas.Monitors))
	if err != nil {
		t.Fatal(err)
	}
	props := doc.(map[string]any)["$defs"].(map[string]any)["monitor"].(map[string]any)["properties"].(map[string]any)
	floor, err := props["max_event_bytes"].(map[string]any)["minimum"].(json.Number).Int64()
	if err != nil {
		t.Fatal(err)
	}
	// Equal to the floor, not merely at or above it: the corpus documents this
	// as the smallest boundary a conforming source can configure, and raising
	// the limit with both files resized to match would keep a below-floor
	// check green while the fixture no longer sat at that boundary.
	if limits.MaxEventBytes != floor {
		t.Errorf("limits.json caps events at %d; the smallest configurable boundary is the schema's floor of %d", limits.MaxEventBytes, floor)
	}
	for name, ok := range map[string]func(int64) bool{
		"exact-limit.jsonl": func(n int64) bool { return n == limits.MaxEventBytes },
		// Exactly one byte past the cap, not merely past it: the pair exists
		// to sit on the boundary, and a padding edit that pushed it further
		// out would still be "over" while no longer testing the edge.
		"over-limit.jsonl": func(n int64) bool { return n == limits.MaxEventBytes+1 },
	} {
		info, err := os.Stat(filepath.Join("events/limits", name))
		if err != nil {
			t.Fatal(err)
		}
		if !ok(info.Size()) {
			t.Errorf("%s is %d bytes against a %d-byte limit", name, info.Size(), limits.MaxEventBytes)
		}
	}
}

// --- state fixtures -------------------------------------------------------
//
// The checkpoint, retention, and cursor documents are the corpus's other
// half: they carry no schema, and their meaning lives in how they relate to
// the event files beside them. Validating events alone left every one of
// those relationships free to drift.

// assertTombstone checks a retention tombstone wherever one appears — in the
// retention document, or as a cursor's creation baseline. Both are compared
// against later removals, so both need the same two fields to compare with.
func assertTombstone(t *testing.T, label string, v any) {
	t.Helper()
	tombstone, ok := v.(map[string]any)
	if !ok {
		t.Errorf("%s is %v, want an object carrying last_removed_id and removed_at", label, v)
		return
	}
	for _, field := range []string{"last_removed_id", "removed_at"} {
		if s, _ := tombstone[field].(string); s == "" {
			t.Errorf("%s carries no %s", label, field)
		}
	}
}

// stateDoc decodes one JSON state fixture. Every one of them — checkpoint,
// retention, cursor — is a per-source document named for its source, so the
// coupling is enforced here rather than in each caller: the member is
// otherwise just a string, and pointing it at another valid slug would leave
// every state guard green while the document described a different spool.
func stateDoc(t *testing.T, path string) map[string]any {
	t.Helper()
	doc, ok := decodeWithNumbers(t, path).(map[string]any)
	if !ok {
		t.Fatalf("%s is not a JSON object", path)
	}
	if source, _ := doc["source"].(string); source+".json" != filepath.Base(path) {
		t.Errorf("%s: document is for source %q", path, source)
	}
	return doc
}

// overflowSequences collects every overflow sequence number appearing in the
// JSONL files under dir.
func overflowSequences(t *testing.T, dir string) []int64 {
	t.Helper()
	var seqs []int64
	prefix := reservedPfx + "overflow:"
	if err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Ext(p) != ".jsonl" {
			return err
		}
		for _, line := range records(t, p) {
			id := recordID(decodeJSON(t, line))
			if !strings.HasPrefix(id, prefix) {
				continue
			}
			n, convErr := strconv.ParseInt(id[strings.LastIndex(id, ":")+1:], 10, 64)
			if convErr != nil {
				t.Errorf("%s: overflow ID %q has no decimal sequence", p, id)
				continue
			}
			seqs = append(seqs, n)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return seqs
}

// TestCheckpointsHoldWatcherOriginIDs pins the rule the synthetic-tail
// fixture exists to demonstrate: a synthetic ID never advances watcher
// ingestion state, so it can never be handed back as --since-id.
func TestCheckpointsHoldWatcherOriginIDs(t *testing.T) {
	for _, path := range glob(t, "ingest/*/ingest/*.json") {
		doc := stateDoc(t, path)
		lastID, _ := doc["last_id"].(string)
		if strings.HasPrefix(lastID, reservedPfx) {
			t.Errorf("%s: checkpoint holds the synthetic ID %q", path, lastID)
		}
		if lastID == "" {
			t.Errorf("%s: checkpoint carries no last_id", path)
		}
	}
}

// TestSyntheticTailCheckpointClearsTheDrop pins the recoverable-overflow
// rule: once the record is committed the checkpoint advances through the last
// dropped ID, so replay resumes past the loss instead of stalling on it.
func TestSyntheticTailCheckpointClearsTheDrop(t *testing.T) {
	doc := stateDoc(t, "ingest/synthetic-tail/ingest/pr-comments.json")
	lines := records(t, "ingest/synthetic-tail/events/pr-comments.jsonl")
	// The overflow has to be the tail, not merely present: the fixture is
	// named for a spool whose last record is synthetic, and a watcher event
	// appended after it would make the checkpoint comparison meaningless
	// while every assertion here still passed.
	var rec struct {
		ID   string `json:"id"`
		Data struct {
			LastDroppedID string `json:"last_dropped_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(lines[len(lines)-1], &rec); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(rec.ID, reservedPfx+"overflow:") {
		t.Fatalf("the spool tail is %q; this fixture models a synthetic tail", rec.ID)
	}
	lastDropped := rec.Data.LastDroppedID
	if lastDropped == "" {
		t.Fatal("the tail overflow record is not the recoverable form; it carries no last_dropped_id")
	}
	if doc["last_id"] != lastDropped {
		t.Errorf("checkpoint is at %v; the overflow record dropped through %q", doc["last_id"], lastDropped)
	}
}

// TestStaleCheckpointLagsItsSpool pins the crash window the fixture models:
// the spool holds an event the checkpoint has not yet recorded.
func TestStaleCheckpointLagsItsSpool(t *testing.T) {
	doc := stateDoc(t, "ingest/stale/ingest/pr-comments.json")
	lines := records(t, "ingest/stale/events/pr-comments.jsonl")
	if len(lines) < 2 {
		t.Fatal("the stale fixture needs an event beyond the checkpoint")
	}
	tail := recordID(decodeJSON(t, lines[len(lines)-1]))
	if doc["last_id"] == tail {
		t.Errorf("checkpoint is caught up at %q; the fixture models a checkpoint left behind", tail)
	}
	var found bool
	for _, line := range lines {
		if recordID(decodeJSON(t, line)) == doc["last_id"] {
			found = true
		}
	}
	if !found {
		t.Errorf("checkpoint %v names no event in the spool", doc["last_id"])
	}
}

// TestMissingCheckpointIsAbsent keeps the missing fixture distinguishable
// from the stale one: its whole point is that no checkpoint document exists.
func TestMissingCheckpointIsAbsent(t *testing.T) {
	if _, err := os.Stat("ingest/missing/ingest"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("ingest/missing carries a checkpoint directory (%v); the fixture models one that was never written", err)
	}
	if len(records(t, "ingest/missing/events/pr-comments.jsonl")) == 0 {
		t.Error("ingest/missing carries no events to rebuild the checkpoint from")
	}
}

// TestRetentionDocumentsMatchTheirSpools pins the high-water mark against the
// records beside it: the counter recovers from the larger of this mark and
// the highest retained overflow sequence, so a mark below what is retained
// would let a sequence be reallocated with different content.
func TestRetentionDocumentsMatchTheirSpools(t *testing.T) {
	for _, path := range glob(t, "*/*/retention/*.json") {
		doc := stateDoc(t, path)
		mark, err := doc["overflow_high_water"].(json.Number).Int64()
		if err != nil {
			t.Errorf("%s: overflow_high_water is not an integer", path)
			continue
		}
		// A mark below the highest retained sequence is not corruption: it is
		// the crash between appending an overflow record and committing the
		// mark, which is why recovery takes max(mark, highest retained) rather
		// than trusting the mark. Requiring mark >= highest would forbid a
		// state the contract guarantees is survivable. What the document owes
		// on its own is a usable counter value; proving the recovery formula
		// needs the implementation that performs it.
		if mark < 0 {
			t.Errorf("%s: overflow high-water mark is %d", path, mark)
		}
		// What the records themselves do owe: a sequence is allocated once per
		// source and never reused, so two retained overflow records sharing
		// one would mean the same number named two different drops.
		seen := map[int64]bool{}
		for _, seq := range overflowSequences(t, filepath.Dir(filepath.Dir(path))) {
			if seen[seq] {
				t.Errorf("%s: overflow sequence %d is allocated twice in this spool", path, seq)
			}
			seen[seq] = true
		}
		tombstone, present := doc["tombstone"]
		if !present {
			t.Errorf("%s: no tombstone member; it is null before any removal, never absent", path)
			continue
		}
		if tombstone == nil {
			continue
		}
		assertTombstone(t, path+" tombstone", tombstone)
		// A tombstone records what retention removed, so naming a still
		// retained event would make the document contradict the spool it
		// accompanies. Ordering comes from the fixtures' own <id>-<n>
		// sequence, as in the multi-segment layout check.
		removed, _ := tombstone.(map[string]any)["last_removed_id"].(string)
		removedSeq, err := strconv.ParseInt(removed[strings.LastIndex(removed, "-")+1:], 10, 64)
		if err != nil {
			t.Errorf("%s: last_removed_id %q does not end in the fixtures' decimal sequence", path, removed)
			continue
		}
		oldest, found := int64(0), false
		for _, events := range glob(t, filepath.Join(filepath.Dir(filepath.Dir(path)), "*.jsonl")) {
			for _, line := range records(t, events) {
				id := recordID(decodeJSON(t, line))
				n, convErr := strconv.ParseInt(id[strings.LastIndex(id, "-")+1:], 10, 64)
				if convErr != nil {
					continue
				}
				if !found || n < oldest {
					oldest, found = n, true
				}
			}
		}
		if found && removedSeq >= oldest {
			t.Errorf("%s: tombstone removed %q, but %q is still retained", path, removed, fmt.Sprintf("pr-%d", oldest))
		}
	}
}

// TestFreshCursorIsBeforeEverything pins the created-but-never-acknowledged
// state: null is the canonical before-everything position, never an empty
// string or a sentinel ID, and the creation tombstone is the baseline later
// retention is measured against.
func TestFreshCursorIsBeforeEverything(t *testing.T) {
	for _, path := range glob(t, "cursors/fresh/*/*/*.json") {
		doc := stateDoc(t, path)
		// last_id and acked_at move together: a cursor created but never
		// acknowledged has neither, and a timestamp beside a null position
		// would be internally inconsistent while satisfying either check
		// on its own.
		for _, field := range []string{"last_id", "acked_at"} {
			if v, present := doc[field]; !present || v != nil {
				t.Errorf("%s: %s is %v; a fresh cursor has acknowledged nothing, so it is present and null", path, field, v)
			}
		}
		if seq, ok := doc["served_seq"].(json.Number); !ok || seq.String() != "0" {
			t.Errorf("%s: served_seq is %v; a new cursor stores 0 for every source", path, doc["served_seq"])
		}
		if list, ok := doc["offer_list"].([]any); !ok || len(list) != 0 {
			t.Errorf("%s: offer_list is %v; nothing has been offered yet", path, doc["offer_list"])
		}
		if v, present := doc["offered_frontier"]; !present || v != nil {
			t.Errorf("%s: offered_frontier is %v; no poll has returned anything yet", path, v)
		}
		// The baseline is compared against later retention, so it has to be a
		// usable tombstone rather than merely present: an empty object or a
		// scalar would satisfy a non-null check and compare against nothing.
		if doc["creation_tombstone"] == nil {
			t.Errorf("%s: no creation tombstone baseline; the null-cursor blind spot needs it", path)
			continue
		}
		assertTombstone(t, path+" creation_tombstone", doc["creation_tombstone"])
	}
}

// TestCursorFairnessOrdering pins the two-source scenario the README
// describes: the lower served_seq is the least recently served and is
// selected first.
func TestCursorFairnessOrdering(t *testing.T) {
	seqs := map[string]int64{}
	for _, path := range glob(t, "cursors/two-sources/*/*/*.json") {
		doc := stateDoc(t, path)
		source, _ := doc["source"].(string)
		n, err := doc["served_seq"].(json.Number).Int64()
		if err != nil {
			t.Fatalf("%s: served_seq is not an integer", path)
		}
		seqs[source] = n
		frontier, present := doc["offered_frontier"]
		if !present {
			t.Errorf("%s: no offered_frontier; an advancing ack is validated against it", path)
			continue
		}
		list, ok := doc["offer_list"].([]any)
		if !ok {
			t.Errorf("%s: offer_list is %v, want an array", path, doc["offer_list"])
			continue
		}
		// The two sources model the two halves of the scenario, so each is
		// pinned to its own half rather than to whatever it happens to hold:
		// pr-comments has an offered batch end still unacknowledged, and
		// ci-status has acknowledged everything it was offered.
		switch source {
		case "pr-comments":
			if len(list) == 0 {
				t.Errorf("%s: empty offer list; this cursor models an offered batch end not yet acknowledged", path)
				continue
			}
			if list[len(list)-1] != frontier {
				t.Errorf("%s: offer list ends at %v, frontier is %v; each poll appends its batch-end ID", path, list[len(list)-1], frontier)
			}
			if doc["last_id"] == frontier {
				t.Errorf("%s: frontier %v equals last_id; nothing would be outstanding", path, frontier)
			}
		case "ci-status":
			if len(list) != 0 {
				t.Errorf("%s: offer list is %v; entries at or before the acknowledged position are pruned on acknowledgement", path, list)
			}
			if frontier != doc["last_id"] {
				t.Errorf("%s: frontier %v does not equal last_id %v; this cursor is caught up", path, frontier, doc["last_id"])
			}
		default:
			t.Errorf("%s: unexpected source %q in the two-source fixture", path, source)
		}
	}
	if len(seqs) != 2 {
		t.Fatalf("expected two per-source cursors, got %d", len(seqs))
	}
	if seqs["ci-status"] >= seqs["pr-comments"] {
		t.Errorf("ci-status served_seq %d is not below pr-comments %d; the fixture demonstrates ci-status being served first",
			seqs["ci-status"], seqs["pr-comments"])
	}
}

// TestLegacyCursorOmitsFairnessFields keeps the legacy fixture legacy: it
// exists to prove the absent fields read as zero.
func TestLegacyCursorOmitsFairnessFields(t *testing.T) {
	for _, path := range glob(t, "cursors/legacy-no-fairness/*/*/*.json") {
		doc := stateDoc(t, path)
		for _, field := range []string{"last_seen_at", "served_seq"} {
			if _, present := doc[field]; present {
				t.Errorf("%s: carries %s; the fixture models a document written before those fields existed", path, field)
			}
		}
		// Only the two fairness fields may be missing. Omitting anything else
		// as well would confound the case: a failure could then mean any
		// absent field rather than legacy fairness decoding specifically.
		for _, field := range []string{"consumer", "instance", "source", "last_id", "acked_at", "offered_frontier", "offer_list", "creation_tombstone"} {
			if _, present := doc[field]; !present {
				t.Errorf("%s: also missing %s; this fixture isolates the absent fairness fields, nothing else", path, field)
			}
		}
	}
}

// TestCursorDirectoriesUseTheInstanceHash pins the one path transformation
// every implementation has to agree on: <safe-instance> is the lowercase hex
// SHA-256 of the opaque instance retained inside the document.
func TestCursorDirectoriesUseTheInstanceHash(t *testing.T) {
	for _, path := range glob(t, "cursors/*/*/*/*.json") {
		doc := stateDoc(t, path)
		instance, _ := doc["instance"].(string)
		if instance == "" {
			t.Errorf("%s: no instance retained in the document", path)
			continue
		}
		digest := sha256.Sum256([]byte(instance))
		if got, want := filepath.Base(filepath.Dir(path)), hex.EncodeToString(digest[:]); got != want {
			t.Errorf("%s:\n  directory %s\n  sha256(%q) %s", path, got, instance, want)
		}
		if consumer, _ := doc["consumer"].(string); consumer != filepath.Base(filepath.Dir(filepath.Dir(path))) {
			t.Errorf("%s: consumer %q does not match its path component", path, consumer)
		}
		// The source/filename coupling is checked for every state document in
		// stateDoc, so it is not repeated here.
	}
}
