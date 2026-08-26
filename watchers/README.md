# watchers/

Watcher executables. A watcher is any executable satisfying the six
requirements in [`../docs/event-contract.md`](../docs/event-contract.md)
§Watcher requirements: bounded JSONL on stdout, pull support via
`--since-id`, idempotent IDs, safe under concurrent invocation, clean
shutdown under supervision, no secrets in output.

Reference watchers (`pr-comments`, `ci-status`) and the generic
`exec-diff`/`file-tail` class-watchers will follow with the runtime.
