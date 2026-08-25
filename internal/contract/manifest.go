package contract

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// Manifest defaults and bounds (event-contract.md §Manifest). The byte
// bounds live next to the event constants in event.go.
const (
	DefaultSeverityFloor = SeverityInfo
	DefaultBatchSize     = 20
	DefaultInterval      = 60 * time.Second
	MinInterval          = 5 * time.Second

	DefaultRotateBytes    = 8388608
	DefaultRetentionBytes = 134217728
	DefaultCursorGrace    = 7 * 24 * time.Hour
	DefaultIdleTimeout    = 5 * time.Minute
)

// The two manifest rules the schema cannot express: one compares entries,
// the other compares two members of one entry.
var (
	ErrDuplicateSource = errors.New("duplicate source name")
	ErrRotateRatio     = errors.New("rotate_bytes exceeds half of retention_bytes")
)

// Tier names one adapter class emitted for a source.
type Tier string

// The four contract tiers (event-contract.md §Manifest).
const (
	TierPush  Tier = "push"
	TierHook  Tier = "hook"
	TierMCP   Tier = "mcp"
	TierProse Tier = "prose"
)

// Monitor is one validated monitors.yaml entry with defaults applied.
type Monitor struct {
	Name          string
	Command       []string
	Description   string
	Trigger       string
	Tiers         []Tier
	Follow        bool
	SeverityFloor Severity
	BatchSize     int
	MaxEventBytes int
	Interval      time.Duration

	// Retention controls (event-contract.md §Rotation, §Manifest).
	RotateBytes    int
	RetentionBytes int
	// RetentionAge is unset by default, which is zero here: the contract
	// declares no default age bound, so a source is under the byte ceiling
	// alone until one is configured.
	RetentionAge time.Duration
	CursorGrace  time.Duration
	IdleTimeout  time.Duration
}

// ParseManifest validates raw monitors.yaml bytes: strict YAML decode
// (duplicate mapping keys rejected by the YAML layer), schema validation
// against schemas/monitors.schema.json, then the cross-entry rules the
// schema cannot express — duplicate and case-folded-alias source names
// (event-contract.md §Manifest). An empty document is rejected; an empty
// manifest is spelled [].
func ParseManifest(data []byte) ([]Monitor, error) {
	var doc any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	if doc == nil {
		return nil, errors.New("manifest must be a YAML array; an empty manifest is []")
	}
	value, err := yamlToJSON(doc)
	if err != nil {
		return nil, err
	}
	if err := compiledSchemas().manifest.Validate(value); err != nil {
		return nil, err
	}

	entries := value.([]any) // schema guarantees the array shape
	monitors := make([]Monitor, 0, len(entries))
	seen := make(map[string]string, len(entries))
	for _, entry := range entries {
		monitor := newMonitor(entry.(map[string]any))
		if err := ValidateSlug("source name", monitor.Name); err != nil {
			return nil, err // defensive double of the schema pattern: this gate guards path construction
		}
		// The ceiling must always leave a whole segment removable, so a
		// rotation threshold above half of it would make retention
		// unenforceable. The schema bounds each field on its own and cannot
		// compare two members of one entry, so the rule lands here.
		if monitor.RotateBytes > monitor.RetentionBytes/2 {
			return nil, fmt.Errorf("source %q: %w (%d against %d)",
				monitor.Name, ErrRotateRatio, monitor.RotateBytes, monitor.RetentionBytes)
		}
		// The slug grammar is ASCII, so simple lowercasing is full case
		// folding here; uppercase never validates, but the alias check must
		// not silently depend on that.
		folded := strings.ToLower(monitor.Name)
		if prior, dup := seen[folded]; dup {
			return nil, fmt.Errorf("%w: %q duplicates %q (names are compared case-folded)", ErrDuplicateSource, monitor.Name, prior)
		}
		seen[folded] = monitor.Name
		monitors = append(monitors, monitor)
	}
	return monitors, nil
}

// newMonitor builds the typed entry from a schema-validated object, so type
// assertions cannot fail and absent optionals take contract defaults.
func newMonitor(obj map[string]any) Monitor {
	command := make([]string, 0, len(obj["command"].([]any)))
	for _, elem := range obj["command"].([]any) {
		command = append(command, elem.(string))
	}
	tiers := make([]Tier, 0, len(obj["tiers"].([]any)))
	for _, elem := range obj["tiers"].([]any) {
		tiers = append(tiers, Tier(elem.(string)))
	}
	monitor := Monitor{
		Name:           obj["name"].(string),
		Command:        command,
		Description:    obj["description"].(string),
		Trigger:        obj["trigger"].(string),
		Tiers:          tiers,
		SeverityFloor:  DefaultSeverityFloor,
		BatchSize:      DefaultBatchSize,
		MaxEventBytes:  DefaultMaxEventBytes,
		Interval:       DefaultInterval,
		RotateBytes:    DefaultRotateBytes,
		RetentionBytes: DefaultRetentionBytes,
		CursorGrace:    DefaultCursorGrace,
		IdleTimeout:    DefaultIdleTimeout,
	}
	if v, present := obj["follow"]; present {
		monitor.Follow = v.(bool)
	}
	if v, present := obj["severity_floor"]; present {
		monitor.SeverityFloor = Severity(v.(string))
	}
	for field, dst := range map[string]*int{
		"batch_size":      &monitor.BatchSize,
		"max_event_bytes": &monitor.MaxEventBytes,
		"rotate_bytes":    &monitor.RotateBytes,
		"retention_bytes": &monitor.RetentionBytes,
	} {
		if v, present := obj[field]; present {
			*dst = mustInt(v)
		}
	}
	for field, dst := range map[string]*time.Duration{
		"interval":      &monitor.Interval,
		"retention_age": &monitor.RetentionAge,
		"cursor_grace":  &monitor.CursorGrace,
		"idle_timeout":  &monitor.IdleTimeout,
	} {
		if v, present := obj[field]; present {
			*dst = time.Duration(mustInt(v)) * time.Second
		}
	}
	return monitor
}

func mustInt(v any) int {
	n, err := v.(json.Number).Int64()
	if err != nil {
		panic(fmt.Sprintf("schema-validated integer does not parse: %v", err))
	}
	return int(n)
}

// yamlToJSON maps a yaml.v3 value tree onto the strict-decode JSON value
// shapes, so schema validation and canonicalization treat YAML and JSON
// input identically. Numbers become json.Number lexemes.
func yamlToJSON(v any) (any, error) {
	switch val := v.(type) {
	case nil, bool, string:
		return val, nil
	case int:
		return json.Number(strconv.Itoa(val)), nil
	case int64:
		return json.Number(strconv.FormatInt(val, 10)), nil
	case uint64:
		return json.Number(strconv.FormatUint(val, 10)), nil
	case float64:
		return json.Number(strconv.FormatFloat(val, 'g', -1, 64)), nil
	case []any:
		out := make([]any, len(val))
		for i, elem := range val {
			conv, err := yamlToJSON(elem)
			if err != nil {
				return nil, err
			}
			out[i] = conv
		}
		return out, nil
	case map[string]any:
		out := make(map[string]any, len(val))
		for key, elem := range val {
			conv, err := yamlToJSON(elem)
			if err != nil {
				return nil, err
			}
			out[key] = conv
		}
		return out, nil
	default:
		return nil, fmt.Errorf("manifest value of type %T is not supported", v)
	}
}
