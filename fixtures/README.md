# Golden fixture corpus

Shared test input for the schemas and the spool implementation. Every
fixture pins a clause of the event contract
([`../docs/event-contract.md`](../docs/event-contract.md)); a contract edit
that touches one of these anchors flags the fixture for review. Files are
byte-exact — do not reformat them.

`corpus_test.go` is the corpus's own guard: it validates every fixture
against the embedded schemas with format assertion enabled, and recomputes
the derived values a hand edit could desynchronize — both synthetic gap IDs
from their canonical derivation arrays, and `same.sha256` from the committed
canonical bytes. Fixtures whose expectation the schema cannot express are
asserted against that expectation directly rather than skipped, so a skip can
never stand in for coverage: `duplicate-keys.jsonl` must still repeat a member
name, `duplicate-source.yaml` must still collide, `same-{a,b}.jsonl` must
still decode to what `same.canonical` describes, the number-lexeme pair must
stay distinguishable, and every optional manifest field must be set by some
valid fixture.

The checkpoint, retention, and cursor documents carry no schema, so their
meaning is entirely in how they relate to the event files beside them, and
the guard asserts those relations directly: a checkpoint never holds a
synthetic ID, the synthetic-tail checkpoint sits exactly at its overflow
record's `last_dropped_id`, the stale checkpoint names a spooled event that
is not the tail, the missing one has no document at all, a retention
high-water mark is never below a retained overflow sequence, the fresh cursor
is null/0/empty with a creation baseline, the legacy cursor still omits the
fairness fields, and every cursor directory is the SHA-256 of the instance
retained inside it.

A case-folded alias between two schema-valid names is unreachable: `name` is
lowercase ASCII by grammar, and two distinct lowercase strings cannot collide
under case folding. `case-folded-alias.yaml` therefore carries an uppercase
name and is rejected by the schema, one step before cross-entry detection
would apply — the validator's own alias rule stays defence in depth for input
that has not been through the schema yet.

Two corpora are missing here on purpose, and are not yet provided anywhere:
the >16 MiB supervision-window stream (§Overflow) and duplicate replay sets
larger than any in-memory cache (§Spool and cursors). Both are too large to
commit, so they will be produced deterministically by a generator that
arrives with the code that needs them.

| Fixture | Contract anchor | Expectation |
| :-- | :-- | :-- |
| `events/valid/*.jsonl` | §Event | parse and validate; `long-summary` additionally emits the ~120-character lint warning (§Why `summary` is constrained) |
| `events/invalid/wrong-severity.jsonl` | §Event severity enum | reject |
| `events/invalid/non-utc-ts.jsonl` | §Event `ts` UTC | reject: numeric offset |
| `events/invalid/bad-calendar-ts.jsonl` | §Event `ts` RFC 3339 | reject: passes the digit pattern, fails calendar validation |
| `events/invalid/empty-source.jsonl`, `uppercase-source.jsonl` | §Event `source` slug | reject before any path is formed |
| `events/invalid/reserved-synthetic-id.jsonl` | §Event reserved prefix | reject on watcher input |
| `events/invalid/duplicate-keys.jsonl` | §Spool and cursors parse profile | reject at decode |
| `events/invalid/multiline-summary.jsonl` | §Event one-line summary | reject |
| `events/invalid/missing-id.jsonl`, `empty-summary.jsonl`, `array-top-level.jsonl` | §Event required fields / top-level object | reject |
| `events/canonical/same-{a,b}.jsonl`, `same.canonical`, `same.sha256` | §Spool and cursors canonical serialization | both digest to `same.sha256`; canonical bytes equal `same.canonical` |
| `events/canonical/lexeme-{int,float}.jsonl` | §Spool number-lexeme preservation | `1` and `1.0` digest differently; `data.n` is the pair's only difference, so nothing else can account for the split |
| `events/canonical/conflict-{a,b}.jsonl` | §Spool duplicate vs conflict | same ID, different digest — hard conflict |
| `events/limits/*` | §Manifest `max_event_bytes`, §Watcher requirement 1 | `exact-limit` (1024 B incl. newline, per `limits.json`) ingests; `over-limit` is one byte longer and rejects before JSON decoding. 1024 is the contract floor for `max_event_bytes`, so this is the smallest boundary a conforming source can configure; the padding sits in `data`, since `summary` is capped at 512 scalars |
| `manifest/valid/*.yaml` | §Manifest | validate; the minimal fixtures leave optionals absent to take defaults, and `all-options` sets every one of them to a non-default value. `empty-command-element` pins the boundary: the argv *array* must be non-empty, an individual argument may be the empty string |
| `manifest/invalid/*.yaml` | §Manifest | reject: unknown tier, traversal name, below-floor interval, above-ceiling bytes, non-array root, and the uppercase name in `case-folded-alias`. Duplicate YAML keys are rejected at decode, and `duplicate-source` by the manifest validator — neither is expressible in the schema |
| `spool/torn-tail/pr-comments.jsonl` | §Spool and cursors durability | reader stops at last `\n`; writer repairs tail on restart |
| `spool/multi-segment/*` | §Rotation | monotonic never-overwritten archives plus active file (exact naming is decided by the spool implementation); `retention/pr-comments.json` carries the tombstone left by the removed oldest segment |
| `synthetic/gap.jsonl` | §Rotation | retained variant: ID is the SHA-256 of the canonical `["gap","retained",…]` array, data carries `cursor_id`/`last_id`/`last_removed_id`/`first_available_id` |
| `synthetic/gap-empty-source.jsonl` | §Rotation | empty-source variant: distinct `["gap","empty",…]` digest input, tombstone-derived `ts`, no `first_available_id`, and `last_id: null` rendered as the literal `-` in the summary |
| `synthetic/overflow-known-ids.jsonl`, `overflow-single.jsonl` | §Overflow | `pending_bytes_exceeded` — the only recoverable reason; summary agrees with `dropped_count` in grammatical number |
| `synthetic/overflow-fingerprint.jsonl`, `overflow-prefix-scope.jsonl` | §Overflow | `line_limit_exceeded` with `dropped_ids_known: false`; `full` may arm a replay skip, `prefix` restores the quarantine |
| `synthetic/overflow-malformed.jsonl` | §Overflow | `malformed_line` — an in-limit line that fails safe parsing takes the same fingerprint path |
| `ingest/synthetic-tail/*` | §Spool ingest checkpoint | the spool tail is an `overflow` record, never a `gap`; the checkpoint holds the last dropped watcher-origin ID (`pr-4`), so a synthetic ID never becomes `--since-id` |
| `ingest/stale/*`, `ingest/missing/*` | §Spool ingest checkpoint | crash between spool sync and checkpoint sync; rebuild by scanning watcher-origin events |
| `cursors/two-sources/*` | §Delivery transaction | one delivery instance, two independent per-source positions; lower `served_seq` (ci-status, 3) is served first; `pr-comments` holds an unacknowledged batch-end ID in its offer list |
| `cursors/fresh/*` | §Cursor lifecycle | first non-replay poll: `last_id: null`, `served_seq: 0`, empty offer list, and a creation tombstone baseline that later retention is measured against |
| `cursors/legacy-no-fairness/*` | §Spool cursor fields | missing `last_seen_at`/`served_seq` reads as zero |

Cursor directories use the real instance hash: `sha256("build-agent-main")`
hex, with the original identity retained inside each document (§Spool and
cursors). Synthetic gap IDs are the real digests of their canonical
derivation arrays, so a fixture edit that changes any component must
recompute the ID.

`retention/<source>.json` is the durable retention metadata document: the
tombstone (last removed ID and removal time, `null` before any removal) and
the overflow high-water mark the per-source sequence counter recovers from
(§Spool and cursors, §Overflow).
