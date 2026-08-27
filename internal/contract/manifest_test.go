package contract

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

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
	want := Monitor{
		Name:           monitor.Name,
		Command:        monitor.Command,
		Description:    monitor.Description,
		Trigger:        monitor.Trigger,
		Tiers:          monitor.Tiers,
		Follow:         false,
		SeverityFloor:  DefaultSeverityFloor,
		BatchSize:      DefaultBatchSize,
		MaxEventBytes:  DefaultMaxEventBytes,
		Interval:       DefaultInterval,
		RotateBytes:    DefaultRotateBytes,
		RetentionBytes: DefaultRetentionBytes,
		// No default age bound is declared, so the source is under the byte
		// ceiling alone until one is configured.
		RetentionAge: 0,
		CursorGrace:  DefaultCursorGrace,
		IdleTimeout:  DefaultIdleTimeout,
	}
	if !monitorsEqual(monitor, want) {
		t.Fatalf("defaults not applied:\n got %+v\nwant %+v", monitor, want)
	}
	if len(monitor.Command) != 3 {
		t.Fatalf("argv not preserved: %v", monitor.Command)
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
	for _, field := range []struct {
		name string
		got  any
		def  any
	}{
		{"follow", monitor.Follow, false},
		{"severity_floor", monitor.SeverityFloor, DefaultSeverityFloor},
		{"batch_size", monitor.BatchSize, DefaultBatchSize},
		{"max_event_bytes", monitor.MaxEventBytes, DefaultMaxEventBytes},
		{"interval", monitor.Interval, DefaultInterval},
		{"rotate_bytes", monitor.RotateBytes, DefaultRotateBytes},
		{"retention_bytes", monitor.RetentionBytes, DefaultRetentionBytes},
		{"retention_age", monitor.RetentionAge, time.Duration(0)},
		{"cursor_grace", monitor.CursorGrace, DefaultCursorGrace},
		{"idle_timeout", monitor.IdleTimeout, DefaultIdleTimeout},
	} {
		// The fixture sets each of these to a non-default value, so a field
		// still holding its default is one the loader never read.
		if field.got == field.def {
			t.Errorf("%s is %v, its default; the loader is not reading the fixture's value", field.name, field.got)
		}
	}
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

// monitorsEqual compares two entries field by field. Monitor holds slices, so
// it is not comparable with ==, and reflect.DeepEqual over the whole struct
// would report a difference without naming the field.
func monitorsEqual(a, b Monitor) bool {
	return a.Name == b.Name && a.Description == b.Description && a.Trigger == b.Trigger &&
		a.Follow == b.Follow && a.SeverityFloor == b.SeverityFloor && a.BatchSize == b.BatchSize &&
		a.MaxEventBytes == b.MaxEventBytes && a.Interval == b.Interval &&
		a.RotateBytes == b.RotateBytes && a.RetentionBytes == b.RetentionBytes &&
		a.RetentionAge == b.RetentionAge && a.CursorGrace == b.CursorGrace &&
		a.IdleTimeout == b.IdleTimeout
}
