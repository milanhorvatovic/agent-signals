# CLI surface (frozen)

The exact command surface of the `agent-signals` binary, reserved before any
runtime exists. Codex records hook trust against the hook command's hash,
so renaming a command or an identity-bearing argument after adapters ship
re-triggers manual trust review in every install. Names below are therefore
frozen: additions are allowed, renames and removals are breaking changes
that require a new major surface.

Normative behaviour: [`event-contract.md`](event-contract.md) §CLI surface.
This file records names and identity semantics only. A name enters here only
once the contract declares it — the freeze reserves the contract's surface, it
never extends it.

```sh
agent-signals watch <source> [--follow] [--format jsonl|model-stream]
agent-signals poll --consumer <type> --instance <id> [--source SOURCE] [--limit N] [--since-id ID] [--format jsonl|hook]
agent-signals ack --consumer <type> --instance <id> --source SOURCE --id ID
agent-signals get --source SOURCE --id ID
agent-signals hook <harness>
agent-signals status
```

## Identity-bearing arguments

| Argument | Rules |
| :-- | :-- |
| `<source>` / `--source` | Canonical lowercase slug matching a `monitors.yaml` name: the anchorless grammar `[a-z0-9][a-z0-9._-]*` required to match the entire value under strict end-of-input semantics — `\A…\z`-style, never `$`, which some engines let match before a trailing newline — and at most 128 Unicode scalars. Validated before any path is formed. |
| `--consumer <type>` | Same slug grammar; names the adapter family (`codex`, `kimi-code`, `opencode`, `mcp`). Validated first, then copied verbatim into the path component and into `cursor_id`; never interpolated into a path unchecked. |
| `--instance <id>` | Opaque, non-empty, at most 256 Unicode scalars and no control characters, rejected before hashing or persistence. Its filesystem form is one exact transformation — the lowercase hex SHA-256 of the instance's UTF-8 bytes — so every implementation derives identical paths, with the original opaque identity retained inside the cursor document (§Spool and cursors). A client with no usable host session identity supplies its durable registration ID here — one registration is one subscription. |
| `--since-id ID` | Requires `--source`; a non-mutating replay override. The ID must be present in the source's retained segments — an absent one is a distinguishable error rather than a guessed position. A cursor-local gap ID is rejected; a retained synthetic overflow ID is a valid position like any other spool record. Distinct from the watcher handoff, where a synthetic spool record never becomes `--since-id`. |
| `--id ID` | On `ack`, an event ID of the one named source or that cursor's deterministic gap ID; which of them advance the cursor and which are idempotent no-ops is §Delivery transaction's validation rule, not a property of the argument. On `get`, an exact retained event ID — an unretained one is a distinguishable error: a retained synthetic overflow record resolves like any other, gap IDs are always rejected. |
| `<harness>` | Names one generated hook adapter, and rides in the hook command string the harness records trust against — which is what makes it identity-bearing. The contract does not enumerate harness names, so no vocabulary is frozen here: each name freezes when its adapter ships, under the additions-allowed policy above. The stable shim reads that harness's hook JSON on stdin and derives `instance` from the harness-supplied session/thread ID rather than taking it as an argument. |

## Semantics anchors (not restated here)

- `watch`: producer-lease acquisition, `--since-id` handoff, shared-spool
  followers, `model-stream` fixed-prefix envelope — event-contract §CLI
  surface.
- `poll`: starvation-free least-recently-served batch, non-blocking,
  non-advancing — §Delivery transaction.
- `ack`: per-source monotonic advance, taking the next `served_seq`. Which
  IDs advance a cursor, which are idempotent no-ops — including a retry
  after a lost acknowledgement response — and how an outstanding gap
  becomes the only advancing acknowledgement are normative there and
  deliberately not enumerated here: this file freezes the argument names,
  not the validation rules — §Delivery transaction, §Rotation.
- `get`: one retained event by exact ID, touching no cursor, frontier, or
  fairness state — §CLI surface.
- `hook`: parse hook stdin, derive instance, hand off, acknowledge accepted
  IDs — §CLI surface.
- `status`: sources, cursor positions, running PIDs — diagnostics only.
