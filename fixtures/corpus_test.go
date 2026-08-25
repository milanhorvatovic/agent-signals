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
func records(t *testing.T, path string) [][]byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	cut := bytes.LastIndexByte(raw, '\n')
	if cut < 0 {
		return nil
	}
	return bytes.Split(bytes.TrimSuffix(raw[:cut], []byte("\n")), []byte("\n"))
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
	// JSON decoder collapses them before the schema ever sees the instance.
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
		// lowercase hex SHA-256 of the instance's UTF-8 bytes.
		parts := strings.SplitN(rec.Data.CursorID, "/", 3)
		if len(parts) != 3 {
			t.Fatalf("%s: cursor_id %q is not <consumer>/<instance>/<source>", path, rec.Data.CursorID)
		}
		instance := sha256.Sum256([]byte(parts[1]))
		var derivation []string
		if rec.Data.FirstAvailableID != "" {
			derivation = []string{"gap", "retained", parts[0], hex.EncodeToString(instance[:]), parts[2], rec.Data.FirstAvailableID}
		} else {
			// Empty-source variant: the record derives from the retention
			// tombstone, and ts is the removal time.
			derivation = []string{"gap", "empty", parts[0], hex.EncodeToString(instance[:]), parts[2], rec.Data.LastRemovedID, rec.TS}
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
	// manifest validator's job, not the schema's.
	validatorSide := map[string]bool{"duplicate-source.yaml": true, "case-folded-alias.yaml": true}
	for _, path := range glob(t, "manifest/invalid/*.yaml") {
		name := filepath.Base(path)
		inst, err := yamlInstance(path)
		if err != nil {
			continue // rejected at YAML decode: duplicate mapping keys
		}
		if err := s.monitors.Validate(inst); err == nil && !validatorSide[name] {
			t.Errorf("%s: accepted, want reject", name)
		}
	}
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
