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
	"os"
	"path/filepath"
	"reflect"
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
	for field := range i["data"].(map[string]any) {
		if field != "n" {
			t.Errorf("data carries %q as well as n; only the lexeme may vary", field)
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
		if err := s.monitors.Validate(inst); err == nil && !validatorSide[name] {
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
