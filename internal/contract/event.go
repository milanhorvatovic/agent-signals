package contract

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/milanhorvatovic/agent-signals/schemas"
)

const (
	// SyntheticIDPrefix is reserved for service-generated events and
	// rejected on watcher input (event-contract.md §Event).
	SyntheticIDPrefix = "agent-signals:synthetic:"
	// The two service-generated record kinds, each with its own ID grammar
	// (event-contract.md §Rotation, §Overflow).
	gapIDPrefix      = SyntheticIDPrefix + "gap:"
	overflowIDPrefix = SyntheticIDPrefix + "overflow:"

	// tsLayout is the contract's whole-second UTC timestamp.
	tsLayout = "2006-01-02T15:04:05Z"

	// DefaultMaxEventBytes bounds one raw JSONL line including its
	// trailing newline (event-contract.md §Manifest).
	DefaultMaxEventBytes = 262144
	// CeilingMaxEventBytes is the hard schema ceiling for max_event_bytes.
	CeilingMaxEventBytes = 1048576

	// summaryTargetRunes is a lint target, never a validity bound
	// (event-contract.md §Why `summary` is constrained).
	summaryTargetRunes = 120
)

// ErrOversized reports a raw line above the source's max_event_bytes,
// detected before any JSON decoding.
var ErrOversized = errors.New("event line exceeds max_event_bytes")

// Severity is the event severity enum (event-contract.md §Event).
type Severity string

// The three contract severities.
const (
	SeverityInfo  Severity = "info"
	SeverityWarn  Severity = "warn"
	SeverityError Severity = "error"
)

// Profile names the stream an event line was read from, which decides what
// may legitimately appear in it.
type Profile int

const (
	// WatcherInput is watcher stdout: the reserved synthetic prefix is
	// rejected.
	WatcherInput Profile = iota
	// SpoolRecord is a line of a source spool. It additionally admits the
	// service's own overflow records, and only those: a gap describes one
	// cursor's position, so it is generated per cursor at delivery time and
	// never appended to the shared spool (event-contract.md §Rotation).
	SpoolRecord
	// DeliveryRecord is a line of delivery output, which carries the gap
	// records alongside everything a spool holds.
	DeliveryRecord
)

// Event is one validated event (event-contract.md §Event).
type Event struct {
	ID       string
	TS       time.Time
	Source   string
	Kind     string
	Severity Severity
	Summary  string
	Data     map[string]any // nil when absent

	// CanonicalDigest identifies the event content for duplicate/conflict
	// detection (§Spool and cursors).
	CanonicalDigest [sha256.Size]byte
}

// ParseEventLine applies the full parse profile to one raw line: byte bound
// before decoding, duplicate-key-rejecting decode, schema validation for the
// given profile, then typed extraction and the canonical content digest.
// maxEventBytes <= 0 selects DefaultMaxEventBytes. Warnings report contract
// lint findings (currently: summary above the ~120-character target) on
// events that are nevertheless valid.
func ParseEventLine(line []byte, maxEventBytes int, profile Profile) (*Event, []string, error) {
	if maxEventBytes <= 0 {
		maxEventBytes = DefaultMaxEventBytes
	}
	if len(line) > maxEventBytes {
		return nil, nil, fmt.Errorf("%w: %d > %d", ErrOversized, len(line), maxEventBytes)
	}
	line = bytes.TrimSuffix(line, []byte("\n"))
	if bytes.ContainsRune(line, '\n') {
		return nil, nil, errors.New("event must be a single line")
	}

	value, err := decodeStrict(line)
	if err != nil {
		return nil, nil, err
	}
	obj, ok := value.(map[string]any)
	if !ok {
		return nil, nil, fmt.Errorf("event must be a JSON object, got %T", value)
	}

	id, _ := obj["id"].(string)
	schema, err := profileSchema(profile, id)
	if err != nil {
		return nil, nil, err
	}
	if err := schema.Validate(value); err != nil {
		return nil, nil, err
	}

	ts, err := parseTimestamp(obj["ts"].(string))
	if err != nil {
		return nil, nil, err
	}
	digest, err := CanonicalDigest(value)
	if err != nil {
		return nil, nil, err
	}
	data, _ := obj["data"].(map[string]any)
	event := &Event{
		ID:              id,
		TS:              ts,
		Source:          obj["source"].(string),
		Kind:            obj["kind"].(string),
		Severity:        Severity(obj["severity"].(string)),
		Summary:         obj["summary"].(string),
		Data:            data,
		CanonicalDigest: digest,
	}

	var warnings []string
	if n := utf8.RuneCountInString(event.Summary); n > summaryTargetRunes {
		warnings = append(warnings, fmt.Sprintf("summary is %d characters; target is ~%d — move detail into data", n, summaryTargetRunes))
	}
	return event, warnings, nil
}

// parseTimestamp reads the contract's whole-second UTC timestamp. The schema
// pattern already pins the lexical shape, but this layer does not lean on
// that: time.Parse adds the calendar validity no pattern can express —
// February 30 sits inside every field's range — and re-rendering closes what
// time.Parse is itself lenient about, since Go accepts a fractional second
// whether or not the layout asks for one.
func parseTimestamp(raw string) (time.Time, error) {
	ts, err := time.Parse(tsLayout, raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("ts: %w", err)
	}
	if ts.Format(tsLayout) != raw {
		return time.Time{}, fmt.Errorf("ts %q is not a whole-second UTC timestamp", raw)
	}
	return ts, nil
}

// profileSchema picks the schema an event must satisfy from its ID and the
// stream it was read from. Each service-generated kind is held to its own
// subschema rather than to the syntheticEvent union: the union is a choice
// between gap and overflow, so validating a spool record against it would
// accept a spooled gap — a record no conforming service ever writes.
func profileSchema(profile Profile, id string) (*jsonschema.Schema, error) {
	schemas := compiledSchemas()
	switch {
	case !strings.HasPrefix(id, SyntheticIDPrefix):
		return schemas.watcherEvent, nil
	case profile == WatcherInput:
		return nil, fmt.Errorf("id prefix %q is reserved for service-generated events", SyntheticIDPrefix)
	case strings.HasPrefix(id, overflowIDPrefix):
		return schemas.overflowEvent, nil
	case !strings.HasPrefix(id, gapIDPrefix):
		return nil, fmt.Errorf("id %q claims the reserved prefix but names no service record kind", id)
	case profile == DeliveryRecord:
		return schemas.gapEvent, nil
	default:
		return nil, fmt.Errorf("id %q is a gap, which is generated per cursor at delivery time and never spooled", id)
	}
}

type schemaSet struct {
	watcherEvent  *jsonschema.Schema
	gapEvent      *jsonschema.Schema
	overflowEvent *jsonschema.Schema
	manifest      *jsonschema.Schema
}

// compiledSchemas compiles the embedded schemas once. Compilation can only
// fail on a broken embedded artifact, which every schema test catches, so
// failure here is a loud panic rather than a threaded error.
var compiledSchemas = sync.OnceValue(func() *schemaSet {
	const (
		eventURL    = "https://github.com/milanhorvatovic/agent-signals/schemas/event.schema.json"
		monitorsURL = "https://github.com/milanhorvatovic/agent-signals/schemas/monitors.schema.json"
	)
	c := jsonschema.NewCompiler()
	c.AssertFormat()
	for url, raw := range map[string][]byte{eventURL: schemas.Event, monitorsURL: schemas.Monitors} {
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
		if err != nil {
			panic(fmt.Sprintf("embedded schema %s: %v", url, err))
		}
		if err := c.AddResource(url, doc); err != nil {
			panic(fmt.Sprintf("embedded schema %s: %v", url, err))
		}
	}
	return &schemaSet{
		watcherEvent:  c.MustCompile(eventURL),
		gapEvent:      c.MustCompile(eventURL + "#/$defs/gapEvent"),
		overflowEvent: c.MustCompile(eventURL + "#/$defs/overflowEvent"),
		manifest:      c.MustCompile(monitorsURL),
	}
})
