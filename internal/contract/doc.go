// Package contract encodes the machine-checkable parts of the event
// contract (docs/event-contract.md): the parse profile for event lines,
// the canonical serialization used for duplicate-content digests,
// identifier validation for values that cross into filesystem paths, and
// manifest loading.
//
// Parse profile (§Event, §Manifest): the raw byte bound — max_event_bytes,
// including the trailing newline — is enforced before any JSON decoding.
// Decoding then rejects what the schema is documented not to cover: input
// that is not valid UTF-8, an escaped unpaired surrogate, nesting past
// MaxDepth, and duplicate object keys at every depth; number lexemes are
// preserved. Schema validation applies schemas/event.schema.json last, and a
// reserved-prefix ID is held to the subschema of its own record kind rather
// than to the union of both, so a gap cannot pass as a spool record. An
// ordinary delivery record answers to the shared field rules alone, since the
// reserved projection marker is legal in delivery output and nowhere else.
//
// Canonical serialization (§Spool and cursors): the digest input for
// duplicate/conflict detection. Byte-exact specification, pinned by the
// fixtures under fixtures/events/canonical:
//
//   - No whitespace between tokens.
//   - Object keys sorted recursively by lexicographic comparison of their
//     UTF-16 code units; the serialization of a duplicate-key object is
//     undefined because the parse profile has already rejected it.
//   - Numbers emitted as their source lexemes, verbatim — 1.0 and 1 stay
//     distinct even though they compare equal as floats.
//   - Strings emitted as UTF-8 under RFC 8785's escape table: `"` and `\`
//     are backslash-escaped; \b \t \n \f \r use their short forms; remaining
//     control characters below U+0020 use lowercase \u00xx. The contract
//     extends that table with U+0085, U+2028, and U+2029, the characters a
//     Unicode-aware line consumer would treat as a line boundary. Everything
//     else, including U+007F and all other non-ASCII, is emitted verbatim.
//   - Literals true, false, null as-is.
//
// This deliberately differs from RFC 8785 (JCS) in two places: JCS
// re-serializes numbers through ES6 float formatting, while this contract
// preserves lexemes so a digest never depends on float round-tripping, and
// JCS leaves the three line-boundary characters raw.
package contract
