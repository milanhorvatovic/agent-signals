# agent-signals
Harness-agnostic bridge that lets a coding agent react to events outside its own turn — PR review comments, CI status changes, failing log lines. Watchers emit JSONL events, a durable spool holds them with at-least-once delivery, and generated adapters deliver them per harness — hooks, MCP, or prose — without rewriting the watcher per agent CLI.
