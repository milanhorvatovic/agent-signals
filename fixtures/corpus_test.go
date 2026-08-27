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
	"os"
	"path/filepath"
	"reflect"
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
	v, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return v
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
	patterns := []string{
		"events/valid/*.jsonl",
		"events/canonical/*.jsonl",
		"events/limits/*.jsonl",
		"spool/*/*.jsonl",
		"ingest/*/events/*.jsonl",
	}
	for _, p := range patterns {
		for _, path := range glob(t, p) {
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
	for _, path := range glob(t, "events/invalid/*.jsonl") {
		name := filepath.Base(path)
		if decodeLevel[name] {
			continue
		}
		if err := s.watcher.Validate(decodeJSON(t, records(t, path)[0])); err == nil {
			t.Errorf("%s: accepted, want reject", name)
		}
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
				Summary string `json:"summary"`
				Data    struct {
					Reason       string `json:"reason"`
					DroppedCount int    `json:"dropped_count"`
				} `json:"data"`
			}
			if err := json.Unmarshal(line, &rec); err != nil {
				t.Fatalf("%s line %d: %v", path, i+1, err)
			}
			if !strings.HasPrefix(rec.ID, reservedPfx+"overflow:") {
				continue
			}
			seen++
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
			ID   string `json:"id"`
			TS   string `json:"ts"`
			Data struct {
				CursorID         string `json:"cursor_id"`
				LastRemovedID    string `json:"last_removed_id"`
				FirstAvailableID string `json:"first_available_id"`
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
		instance := sha256.Sum256([]byte(rec.Data.CursorID[head+1 : tail]))
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
	if get(i) == get(f) {
		t.Errorf("both lexeme fixtures carry n as %q; the pair must stay distinguishable", get(i))
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
		if err == nil {
			t.Errorf("%s: accepted, want reject", name)
		}
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
	set := map[string]bool{}
	for _, path := range glob(t, "manifest/valid/*.yaml") {
		inst, err := yamlInstance(path)
		if err != nil {
			t.Fatalf("%s: %v", filepath.Base(path), err)
		}
		entries, _ := inst.([]any)
		for _, e := range entries {
			for field := range e.(map[string]any) {
				set[field] = true
			}
		}
	}
	for field := range monitor["properties"].(map[string]any) {
		if !required[field] && !set[field] {
			t.Errorf("no valid fixture sets the optional field %q", field)
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
	if limits.MaxEventBytes < floor {
		t.Errorf("limits.json caps events at %d, below the schema's max_event_bytes floor of %d", limits.MaxEventBytes, floor)
	}
	for name, ok := range map[string]func(int64) bool{
		"exact-limit.jsonl": func(n int64) bool { return n == limits.MaxEventBytes },
		"over-limit.jsonl":  func(n int64) bool { return n > limits.MaxEventBytes },
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

// stateDoc decodes one JSON state fixture.
func stateDoc(t *testing.T, path string) map[string]any {
	t.Helper()
	doc, ok := decodeWithNumbers(t, path).(map[string]any)
	if !ok {
		t.Fatalf("%s is not a JSON object", path)
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
	var lastDropped string
	for _, line := range records(t, "ingest/synthetic-tail/events/pr-comments.jsonl") {
		var rec struct {
			ID   string `json:"id"`
			Data struct {
				LastDroppedID string `json:"last_dropped_id"`
			} `json:"data"`
		}
		if err := json.Unmarshal(line, &rec); err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(rec.ID, reservedPfx+"overflow:") {
			lastDropped = rec.Data.LastDroppedID
		}
	}
	if lastDropped == "" {
		t.Fatal("the synthetic tail carries no recoverable overflow record")
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
		var highest int64
		for _, seq := range overflowSequences(t, filepath.Dir(filepath.Dir(path))) {
			if seq > highest {
				highest = seq
			}
		}
		if mark < highest {
			t.Errorf("%s: high-water mark %d is below the retained overflow sequence %d", path, mark, highest)
		}
		tombstone, present := doc["tombstone"]
		if !present {
			t.Errorf("%s: no tombstone member; it is null before any removal, never absent", path)
			continue
		}
		if tombstone != nil {
			assertTombstone(t, path+" tombstone", tombstone)
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
		if v, present := doc["last_id"]; !present || v != nil {
			t.Errorf("%s: last_id is %v; a fresh cursor sits before everything as JSON null", path, v)
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
		if source, _ := doc["source"].(string); source+".json" != filepath.Base(path) {
			t.Errorf("%s: source %q does not match its filename", path, source)
		}
	}
}
