# agent-signals

A harness-agnostic bridge that lets a coding agent react to events outside
its own turn — new PR review comments, CI status changes, failing log lines —
without rewriting the watcher for every agent CLI.

One executable emits events, one durable spool holds them, thin generated
adapters deliver them per harness. The normative contract every watcher and
adapter agrees on is [`docs/event-contract.md`](docs/event-contract.md).

**Status:** early development. The contract, its schemas, the golden fixture
corpus, and the build scaffold — no runtime behaviour yet.

## Development

Toolchain is pinned in `mise.toml` ([mise](https://mise.jdx.dev)):

```sh
mise install
make check   # lint + test + build
```

The spool targets local macOS and Linux filesystems only; network
filesystems (NFS) and native Windows are unsupported.
