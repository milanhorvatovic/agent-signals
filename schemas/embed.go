// Package schemas embeds the normative JSON Schemas so the binary never
// depends on files resolved at run time. The .schema.json files are the
// reviewable source of truth; this package only carries their bytes.
package schemas

import _ "embed"

// Event is schemas/event.schema.json (event-contract.md §Event).
//
//go:embed event.schema.json
var Event []byte

// Monitors is schemas/monitors.schema.json (event-contract.md §Manifest).
//
//go:embed monitors.schema.json
var Monitors []byte
