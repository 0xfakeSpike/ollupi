<h1>
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="https://raw.githubusercontent.com/0xfakeSpike/ollupi-desktop/main/assets/brand/logo-dark.svg">
    <img alt="Ollupi" src="https://raw.githubusercontent.com/0xfakeSpike/ollupi-desktop/main/assets/brand/logo.svg" width="240">
  </picture>
</h1>

[English](README.md) | [简体中文](README.zh-CN.md)

Ollupi is a Go runtime for durable, multi-tenant agents. It provides crash recovery for agent sessions: conversation state is persisted to SQLite, and runnable turns are reconstructed and resumed after a process restarts. In a sharded deployment, a standby process can also take over a shard through its lease. Tool calls can run asynchronously in a dedicated tool pool with independent concurrency limits, so long-running work does not occupy a conversation slot.

## What you can build

- **A local coding agent.** Give it a project folder, code-editing and command tools, approvals, memory, and specialized subagents.
- **A multi-user agent backend.** Serve many users from one shard, give each user separate models, sandboxes, connections, quotas, and stored history.
- **Durable long-running work.** Queue chats, background commands, subagents, and scheduled jobs without tying each task to a process or VM.

The same engine runs a desktop-scale deployment and a sharded server. [ollupi-desktop](https://github.com/0xfakeSpike/ollupi-desktop) is the desktop client built on it.

![Ollupi architecture](docs/images/architecture.svg)

## Core capabilities

| Area | What Ollupi provides |
|---|---|
| Crash recovery | Append-only conversation logs; after a process restart, the scheduler reconstructs and resumes runnable turns |
| Asynchronous tools | Tools that support background execution run outside the turn in a dedicated pool with global and per-user concurrency limits; their reports return to the originating turn |
| Scheduling | Session execution is pooled with per-user limits, time limits, and fair ordering; sharded deployments can fail over to a standby process |
| Models | OpenAI-compatible, Anthropic, and Gemini clients; per-connection concurrency, retries, and per-user model sets |
| Tools and context | File/read/command tools, MCP servers, skills, memory recall and extraction, compaction, and attachments |
| Agent workflows | Specialized subagents, a hub for coordinating projects, todos, scheduled jobs, plans, and user questions |
| Isolation and policy | Per-user sandboxed commands and files, layered permission rules, approvals, and full-access escalation |
| Serving | Authenticated WebSocket protocol, multiple devices, reconnect guidance, usage and presence queries |
| Scale-out | SQLite shards, leases, failover epochs, catalogs, load-aware placement, and an Envoy external processor |

## Why pooled agents

| | One VM or container per agent | Ollupi |
|---|---|---|
| One process hosts | One session | All sessions |
| Inactive sessions | Process stays up, or is rebuilt with a cold start | Database records only |
| Waiting on a user or approval | Process sits suspended, still holding memory | Gives its slot back; only database records remain |
| Session state | Process memory | SQLite |
| After a restart | In-progress work is interrupted | Continues from where it stopped |
| Long tool call | Holds the process or slot until it returns | Runs in the background tool pool; the session's slot is free |
| Process failure with a standby | Depends on the surrounding orchestration | A standby acquires the shard lease and takes over |
| Call cut off by a crash | Redone or lost | Marked possibly applied, never re-run |
| Subagents | Special mechanism or child process | Ordinary sessions, queued in the session pool |
| Scheduled jobs | External cron starts a process | A message to the session when due, queued in the session pool |

![Per-agent isolation compared with Ollupi](docs/images/cli-vs-ollupi.svg)

## Evidence

The benchmark suite exercises the real host, SQLite store, scheduler, and gateway with simulated models and tools. It measures scheduling and durability, not model quality. The figures below are from v0.18.0; method, caveats, raw output, and reproduction commands are in [bench](bench/README.md).

| Workload | Result |
|---|---|
| 3,000 turns submitted at once with a provider quota of 256 | 3,000 completed, no 429 responses |
| 50,000 sessions parked on users | 34 MB resident memory overall, about 7 KB of disk per session |
| Repeated `kill -9` during active turns and tools | Every accepted turn completes; no tool call runs twice |
| `kill -9` while asynchronous background commands run | 200 of 200 calls report back, each marked cut off where appropriate |

Caveat: above the measured 1,536 sessions, turn latency rises while the store waits on its one SQLite connection; at 4,096 sessions it was 2.8 times the solo latency. Sharding is the scale-out path.

![Comparison with pi and DeepSeek Harness](docs/images/bench-compare.svg)

## Execution model

There are two schedulers. The session pool runs turns from users, subagents, and scheduled jobs on a fixed number of slots. Tools that support asynchronous execution, such as `run_command` and `claude` with `background: true`, run in a separate tool pool with their own global and per-user concurrency limits. When the call finishes, its report returns to the originating turn. Model concurrency is limited per provider connection and is separate from tool execution. A turn waiting for a user, approval, subagent, or background report releases its slot.

Each session is an append-only log. Before a tool runs, its intent is written to SQLite; model responses and tool results are appended as they finish. A process restart reconstructs schedulable work from the database. A tool that started but never recorded a result is surfaced as cut off and is never automatically run twice. One change feed drives clients, scheduling, and memory extraction.

More detail: [workspace, conversation, turn, and item lifecycle](docs/lifecycle.md).

## Multi-user deployment and shards

One SQLite database is one shard. Users share a shard but remain isolated by user, and a shard process holds a renewable lease in a separate catalog. When a process exits, a standby acquires the shard lease and recovers its session work from the database. This is the cross-process failover path that complements agent-level crash recovery. Lease epochs prevent a stalled former holder from writing after a new holder starts. A router places users on accepting shards and publishes the selected shard to an Envoy external processor when one is used.

Commands, files, and MCP servers are reached through `sandbox.Box`. A deployment offers sandboxes as providers (`sandbox.Provider` in `host.Config.Sandboxes`): this machine is one, `sandbox.Local`, and a cloud sandbox plugs in the same way, its machine behind `sandbox.Remote`. Each environment a user has is one sandbox of a provider, made the first time a conversation needs it and found again after a restart from the state the provider keeps. Every user starts with one environment of the first provider; a workspace is a folder of one environment, and a conversation keeps the environment it started in. Model traffic may use each user's own connection, and attachments are stored under the host's `attachments` directory.

## Security and operating limits

- `sandbox.Local` in `full-access` mode, and `sandbox.Here`, run commands with full access to the machine. Use them only for users who may use the whole machine.
- Under the `never` approval policy, an escalated command runs without asking. Do not offer that policy to untrusted users.
- A repository's `.git` is writable inside the sandbox. Hooks written by a command may later run outside those limits when the user invokes Git.
- SQLite is the measured bottleneck for very large single-shard histories and concurrency. Choose shard count from your own workload and keep in mind that each shard writes through one connection (reads use a small read-only pool).
- The benchmark numbers date from v0.18.0 and were measured on one Apple M1. Re-run [bench](bench/README.md) for deployment-specific capacity.

## Documentation

- [Benchmarks and reproduction](bench/README.md)
- [Terminal-Bench evaluation workflow](eval/README.md)
- [Workspace, conversation, turn, and item lifecycle](docs/lifecycle.md)
- [WebSocket protocol schema](gateway/protocol.schema.json)
- [Full user and deployment documentation](https://github.com/0xfakeSpike/ollupi-docs)

Build and test with SQLite FTS5:

```bash
go test -tags sqlite_fts5 ./...
```

## License

[MIT](LICENSE)
