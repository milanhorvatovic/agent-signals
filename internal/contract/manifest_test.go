package contract

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"go.yaml.in/yaml/v3"

	"github.com/milanhorvatovic/agent-signals/schemas"
)

func TestValidManifestFixtures(t *testing.T) {
	for _, path := range globFixtures(t, "manifest", "valid", "*.yaml") {
		t.Run(filepath.Base(path), func(t *testing.T) {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ParseManifest(data); err != nil {
				t.Fatalf("valid manifest rejected: %v", err)
			}
		})
	}
}

// TestInvalidManifestFixtures holds each negative fixture to the layer that
// rejects it. Two of them are schema-valid by design and exist only for the
// rules this loader adds; without the layer check they would keep passing if
// those rules were deleted and the documents happened to fail for some other
// reason.
func TestInvalidManifestFixtures(t *testing.T) {
	// The layer each fixture belongs to. Anything unlisted is schema-level.
	validatorSide := map[string]error{
		"duplicate-source.yaml":              ErrDuplicateSource,
		"rotate-exceeds-half-retention.yaml": ErrRotateRatio,
	}
	const decodeLevel = "duplicate-yaml-keys.yaml"
	for _, path := range globFixtures(t, "manifest", "invalid", "*.yaml") {
		name := filepath.Base(path)
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			_, err = ParseManifest(data)
			if err == nil {
				t.Fatal("invalid manifest accepted")
			}
			var schemaErr *jsonschema.ValidationError
			bySchema := errors.As(err, &schemaErr)
			switch rule, ok := validatorSide[name]; {
			case ok:
				if !errors.Is(err, rule) {
					t.Fatalf("rejected by %v, want %v — this fixture is schema-valid and exists for that rule alone", err, rule)
				}
			case name == decodeLevel:
				if bySchema {
					t.Fatalf("rejected by schema validation: %v; this fixture must fail at YAML decode", err)
				}
			case !bySchema:
				t.Fatalf("rejected before schema validation: %v", err)
			}
		})
	}
}

func TestManifestDefaults(t *testing.T) {
	monitors, err := ParseManifest(readFixture(t, "manifest", "valid", "minimal.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(monitors) != 1 {
		t.Fatalf("got %d monitors, want 1", len(monitors))
	}
	monitor := monitors[0]
	// The required fields are compared against the fixture read without the
	// loader. Building the expectation from the loaded entry would make it
	// agree with itself: every one of these could be dropped or swapped with
	// another string field and nothing would notice.
	declared := declaredEntry(t, "manifest", "valid", "minimal.yaml")
	for _, field := range []struct {
		key string
		got string
	}{
		{"name", monitor.Name},
		{"description", monitor.Description},
		{"trigger", monitor.Trigger},
	} {
		if want, _ := declared[field.key].(string); field.got != want {
			t.Errorf("%s loaded as %q, the fixture declares %q", field.key, field.got, want)
		}
	}
	// Both slices by content, not by length: an argv of the right size holding
	// the wrong words is the failure a count cannot see, and tiers were
	// compared against nothing at all.
	if want := declaredStrings(t, declared["command"]); !slices.Equal(monitor.Command, want) {
		t.Errorf("command loaded as %q, the fixture declares %q", monitor.Command, want)
	}
	var wantTiers []Tier
	for _, tier := range declaredStrings(t, declared["tiers"]) {
		wantTiers = append(wantTiers, Tier(tier))
	}
	if !slices.Equal(monitor.Tiers, wantTiers) {
		t.Errorf("tiers loaded as %v, the fixture declares %v", monitor.Tiers, wantTiers)
	}
	// This fixture sets no optional field, so every one of them must hold the
	// contract default.
	for _, field := range []struct {
		name string
		got  any
		want any
	}{
		{"follow", monitor.Follow, false},
		{"severity_floor", monitor.SeverityFloor, DefaultSeverityFloor},
		{"batch_size", monitor.BatchSize, DefaultBatchSize},
		{"max_event_bytes", monitor.MaxEventBytes, DefaultMaxEventBytes},
		{"interval", monitor.Interval, DefaultInterval},
		{"rotate_bytes", monitor.RotateBytes, DefaultRotateBytes},
		{"retention_bytes", monitor.RetentionBytes, DefaultRetentionBytes},
		// No default age bound is declared, so a source is under the byte
		// ceiling alone until one is configured.
		{"retention_age", monitor.RetentionAge, time.Duration(0)},
		{"cursor_grace", monitor.CursorGrace, DefaultCursorGrace},
		{"idle_timeout", monitor.IdleTimeout, DefaultIdleTimeout},
	} {
		if _, set := declared[field.name]; set {
			t.Errorf("minimal.yaml sets %s; this fixture exists to leave every optional at its default", field.name)
			continue
		}
		if field.got != field.want {
			t.Errorf("%s defaulted to %v, want %v", field.name, field.got, field.want)
		}
	}
}

// TestManifestCarriesEveryOptionalField reads the fixture whose entry sets
// every optional field to a non-default value. Loading it through the
// defaults test alone would not notice a field the loader ignores: an
// unread member simply keeps its default and the entry still validates.
func TestManifestCarriesEveryOptionalField(t *testing.T) {
	monitors, err := ParseManifest(readFixture(t, "manifest", "valid", "all-options.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(monitors) != 1 {
		t.Fatalf("got %d monitors, want 1", len(monitors))
	}
	monitor := monitors[0]
	// The fixture is read a second time without the loader, so each field is
	// compared against the value under its own manifest key. Comparing only
	// against the defaults would leave two same-typed fields free to swap —
	// exchange retention_age and cursor_grace in the loader and both still
	// differ from their defaults, and every check stays green.
	declared := declaredEntry(t, "manifest", "valid", "all-options.yaml")
	seconds := func(v any) any { return time.Duration(v.(int)) * time.Second }
	for _, field := range []struct {
		name string
		got  any
		def  any
		// want converts the fixture's raw YAML value into the representation
		// the loaded field holds.
		want func(any) any
	}{
		{"follow", monitor.Follow, false, func(v any) any { return v }},
		{"severity_floor", monitor.SeverityFloor, DefaultSeverityFloor, func(v any) any { return Severity(v.(string)) }},
		{"batch_size", monitor.BatchSize, DefaultBatchSize, func(v any) any { return v.(int) }},
		{"max_event_bytes", monitor.MaxEventBytes, DefaultMaxEventBytes, func(v any) any { return v.(int) }},
		{"interval", monitor.Interval, DefaultInterval, seconds},
		{"rotate_bytes", monitor.RotateBytes, DefaultRotateBytes, func(v any) any { return int64(v.(int)) }},
		{"retention_bytes", monitor.RetentionBytes, DefaultRetentionBytes, func(v any) any { return int64(v.(int)) }},
		{"retention_age", monitor.RetentionAge, time.Duration(0), seconds},
		{"cursor_grace", monitor.CursorGrace, DefaultCursorGrace, seconds},
		{"idle_timeout", monitor.IdleTimeout, DefaultIdleTimeout, seconds},
	} {
		raw, present := declared[field.name]
		if !present {
			t.Errorf("all-options.yaml does not set %s; this fixture exists to set every optional field", field.name)
			continue
		}
		if want := field.want(raw); field.got != want {
			t.Errorf("%s loaded as %v, the fixture declares %v", field.name, field.got, want)
		}
		// And the fixture's value must stay off the default, or the comparison
		// above would hold for a field the loader never read.
		if field.got == field.def {
			t.Errorf("%s is %v, its default; the fixture must exercise a non-default value", field.name, field.got)
		}
	}
}

// declaredEntry reads a single-entry manifest fixture with a plain YAML
// unmarshal — deliberately not through ParseManifest, since a loader checked
// against its own output would agree with itself whatever it did.
func declaredEntry(t *testing.T, parts ...string) map[string]any {
	t.Helper()
	var doc []map[string]any
	if err := yaml.Unmarshal(readFixture(t, parts...), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc) != 1 {
		t.Fatalf("%v carries %d entries, want exactly one", parts, len(doc))
	}
	return doc[0]
}

// TestManifestRejectsMultipleDocuments pins the whole input as the unit of
// validation. yaml.Unmarshal reads the first document of a stream and stops,
// so a manifest continuing after `---` was accepted with its later sources
// silently unsupervised — no error, and nothing downstream to notice.
func TestManifestRejectsMultipleDocuments(t *testing.T) {
	one := readFixture(t, "manifest", "valid", "minimal.yaml")
	// The first document is a fixture the suite already parses on its own, so
	// a failure here is the second document and not a broken first one.
	if _, err := ParseManifest(one); err != nil {
		t.Fatalf("the single-document half of this input does not parse: %v", err)
	}
	stream := append(append([]byte{}, one...), []byte("---\n- name: unsupervised\n  command: [\"./w.sh\"]\n  description: a source the first document does not declare\n  trigger: session-start\n  tiers: [mcp]\n")...)
	monitors, err := ParseManifest(stream)
	if !errors.Is(err, ErrMultipleDocuments) {
		t.Fatalf("got %v (%d monitors), want %v", err, len(monitors), ErrMultipleDocuments)
	}
}

// TestRetentionBytesHoldsTheSchemaCeiling guards the width of the byte
// fields. The schema's retention ceiling is above what a 32-bit int can
// hold, so on such a build a narrowed field truncates the limit and the
// rotate ratio computed from it. This passes trivially on a 64-bit builder,
// which is the honest scope: the defect is platform-dependent and so is the
// check that catches it.
func TestRetentionBytesHoldsTheSchemaCeiling(t *testing.T) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schemas.Monitors))
	if err != nil {
		t.Fatal(err)
	}
	props := doc.(map[string]any)["$defs"].(map[string]any)["monitor"].(map[string]any)["properties"].(map[string]any)
	ceiling, err := props["retention_bytes"].(map[string]any)["maximum"].(json.Number).Int64()
	if err != nil {
		t.Fatal(err)
	}
	if ceiling <= math.MaxInt32 {
		t.Fatalf("the retention ceiling is %d, within a 32-bit int; this test covers nothing", ceiling)
	}
	manifest := fmt.Sprintf("- name: pr-comments\n  command: [\"./w.sh\"]\n  description: retention at the schema ceiling\n  trigger: session-start\n  tiers: [mcp]\n  retention_bytes: %d\n", ceiling)
	monitors, err := ParseManifest([]byte(manifest))
	if err != nil {
		t.Fatal(err)
	}
	if monitors[0].RetentionBytes != ceiling {
		t.Errorf("retention_bytes loaded as %d, the manifest declares %d", monitors[0].RetentionBytes, ceiling)
	}
}

func TestManifestExplicitValues(t *testing.T) {
	monitors, err := ParseManifest(readFixture(t, "manifest", "valid", "two-sources.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(monitors) != 2 {
		t.Fatalf("got %d monitors, want 2", len(monitors))
	}
	ciStatus := monitors[1]
	if ciStatus.SeverityFloor != SeverityWarn || ciStatus.BatchSize != 5 || ciStatus.Interval != 120*time.Second {
		t.Fatalf("explicit values lost: %+v", ciStatus)
	}
}

func TestEmptyManifest(t *testing.T) {
	monitors, err := ParseManifest(readFixture(t, "manifest", "valid", "empty.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(monitors) != 0 {
		t.Fatalf("got %d monitors, want 0", len(monitors))
	}
}

// The scaffolded root manifest must always satisfy its own schema.
func TestRepoManifestValidates(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "monitors.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseManifest(data); err != nil {
		t.Fatalf("monitors.yaml does not validate: %v", err)
	}
}

// declaredStrings reads a YAML sequence of strings from a fixture entry.
func declaredStrings(t *testing.T, raw any) []string {
	t.Helper()
	elems, ok := raw.([]any)
	if !ok {
		t.Fatalf("fixture value is %v, want a sequence", raw)
	}
	out := make([]string, 0, len(elems))
	for _, elem := range elems {
		s, ok := elem.(string)
		if !ok {
			t.Fatalf("fixture sequence carries %v, want a string", elem)
		}
		out = append(out, s)
	}
	return out
}
