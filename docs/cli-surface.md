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
| `<source>` / `--source` | Canonical lowercase slug (`^[a-z0-9][a-z0-9._-]*$`, ≤128 bytes) matching a `monitors.yaml` name; validated before any path is formed. |
| `--consumer <type>` | Same slug grammar; names the adapter family (`codex`, `kimi-code`, `opencode`, `mcp`). Never interpolated into a path unchecked. |
| `--instance <id>` | Opaque, non-empty, at most 256 Unicode scalars and no control characters, rejected before hashing or persistence; hashed (SHA-256) for filesystem use, original retained inside the cursor document (§Spool and cursors). A client with no usable host session identity supplies its durable registration ID here — one registration is one subscription. |
| `--since-id ID` | Requires `--source`; a non-mutating replay override. A cursor-local gap ID is rejected; a retained synthetic overflow ID is a valid position like any other spool record. Distinct from the watcher handoff, where a synthetic spool record never becomes `--since-id`. |
| `--id ID` | On `ack`, the last accepted event of one named source, or the outstanding deterministic gap ID for that cursor. On `get`, an exact retained event ID: a retained synthetic overflow record resolves like any other, gap IDs are always rejected. |
| `<harness>` | One fixed name per generated hook adapter, drawn from the adapter families the contract declares (`codex`, `kimi-code`, `opencode`); the stable shim reads the harness's hook JSON on stdin and derives the instance itself. |

## Semantics anchors (not restated here)

- `watch`: producer-lease acquisition, `--since-id` handoff, shared-spool
  followers, `model-stream` fixed-prefix envelope — event-contract §CLI
  surface.
- `poll`: starvation-free least-recently-served batch, non-blocking,
  non-advancing — §Delivery transaction.
- `ack`: per-source monotonic advance, idempotent no-op on current/older
  IDs, `served_seq` update — §Delivery transaction.
- `get`: one retained event by exact ID, touching no cursor, frontier, or
  fairness state — §CLI surface.
- `hook`: parse hook stdin, derive instance, hand off, acknowledge accepted
  IDs — §CLI surface.
- `status`: sources, cursor positions, running PIDs — diagnostics only.
