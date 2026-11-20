<h1>
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="https://raw.githubusercontent.com/0xfakeSpike/ollupi-desktop/main/assets/brand/logo-dark.svg">
    <img alt="Ollupi" src="https://raw.githubusercontent.com/0xfakeSpike/ollupi-desktop/main/assets/brand/logo.svg" width="240">
  </picture>
</h1>

Ollupi is a Go runtime that pools and schedules agents. Give it any number of agent sessions — a user's chats, a thousand subagents, scheduled jobs — and it runs them within fixed limits and queues the rest. Sessions, model calls and tool calls each have their own pool and limit. Every step is written to SQLite before it runs, so a waiting session costs a few rows, survives restarts, and resumes in any process. The same engine runs one person's sessions on a laptop and a provider's users across shards.

- **Sessions are pooled.** A fixed number of slots, with a cap per user; sessions beyond that queue. A session waiting on a user, an approval, a subagent or a background call gives its slot back.
- **Tool calls are pooled.** A long call such as `run_command` or `claude` can run in the background, in a tool pool with its own limits; the turn goes on, and the result comes back as a report.
- **Model calls are rate-limited** per provider connection, apart from tool execution, so long tools never starve model calls.
- **Commands run in a sandbox; the runtime is shared.** Each user's commands, files and MCP servers are reached through `sandbox.Box`, and a cloud sandbox plugs in by implementing `sandbox.Machine`.
- **Nothing runs twice.** A tool call is recorded as started before it runs; a call cut off by a crash is marked possibly applied and handed to the model and user, never re-run.

Sessions, subagents and scheduled jobs queue in the session pool, background calls in the tool pool. [ollupi-desktop](https://github.com/0xfakeSpike/ollupi-desktop) is the desktop client built on Ollupi.

![Ollupi architecture](docs/images/architecture.svg)

## How it differs from per-agent isolation

| | One VM or container per agent | Ollupi |
|---|---|---|
| One process hosts | One session | All sessions |
| Inactive sessions | Process stays up, or is rebuilt with a cold start | Database records only |
| Waiting on a user or approval | Process sits suspended, still holding memory | Gives its slot back; only database records remain |
| Session state | Process memory | SQLite |
| After a restart | In-progress work is interrupted | Continues from where it stopped |
| Long tool call | Holds the process or slot until it returns | Runs in the background tool pool; the session's slot is free |
| Call cut off by a crash | Redone or lost | Marked possibly applied, never re-run |
| Subagents | Special mechanism or child process | Ordinary sessions, queued in the session pool |
| Scheduled jobs | External cron starts a process | A message to the session when due, queued in the session pool |

![Per-agent isolation compared with Ollupi](docs/images/cli-vs-ollupi.svg)

## Benchmarks

Measured on v0.18.0 with a simulated model and tools: they measure scheduling and persistence, not model quality. Method, every number and the commands to reproduce them are in [bench](bench/README.md).

![Comparison with pi and DeepSeek Harness](docs/images/bench-compare.svg)

![Capacity](docs/images/bench-capacity.svg)

![Background calls](docs/images/bench-background.svg)

![Recovery](docs/images/bench-recovery.svg)

![Users](docs/images/bench-users.svg)

## Execution model

![Session pool and tool pool](docs/images/execution-pool.svg)

There are two pools, each with its own scheduler and limits. The session pool runs turns: every runnable turn in every session, whether it comes from a user, a subagent or a scheduled job, enters its queue and runs on a fixed number of slots (`host.Config` `Slots`, 32 by default; `UserSlots` per user, the same as `Slots` unless set; `RunLimit` per run, 4 hours). The tool pool runs background calls: 32 slots (`TaskSlots`), 4 per user (`UserTaskSlots`), and 1 hour per call (`TaskLimit`), oldest first. A tool that allows it (`run_command`, `claude`) runs in the background when the model passes `background: true`; the turn goes on, and once it has answered it waits on the call's report. A call run inside the turn keeps the turn's slot. A turn waiting on a user, an approval, a subagent or a background call's report releases its slot, holds no goroutine or model concurrency, and becomes ready again when the wait ends. When slots run short, ordinary sessions go before subagents, and turns of the same kind run in the order they became ready. Model concurrency is limited per connection (4 per connection by default), and tool execution uses none of it. Model requests that hit a 429, a 5xx or a dropped connection retry with backoff, up to 8 times, keeping their slot and model concurrency while they wait.

Each session is an append-only log: a turn's state, what it waits on and what runs next are read from the log, every step is appended before it runs, and no session state lives in memory; only which session holds which slot, rebuilt from the database. After a process restart the same logic picks up in-progress turns. A tool call appended but not started runs as usual; one started without a result may have taken effect, so it is ended as cut off with a prompt to verify, and is never run twice. Likewise, a background call an earlier process started is reported as cut off, never run twice. Everything a commit changes reaches clients, the scheduler and memory extraction through one change feed, numbered by commit.

## Multi-user and shards

One database is one shard. Users in a shard share tables and are isolated per user. The engine knows nothing of shards: package `shard` runs a process as one. A `shard.Member` holds the shard's lease in the deployment's catalog, which publishes the holder's address with it, so only one process runs a shard at a time; when that process exits, a standby that waits on the lease takes over and continues from where it stopped. Each taking of a shard has an epoch above every one before it, which the shard's database keeps and checks on every write, so a process that stalled past its lease cannot write over the one that took the shard. The member reports the shard's load as it renews the lease, by a measure the deployment chooses (users active of late, turns under way, connections), and runs the engine only while it holds the shard. The number of shards sets capacity. Clients reach one address and never learn which shard they are on: where a load balancer can call out over Envoy's external processing protocol, as Google Cloud's Application Load Balancer does with a route extension, `router/extproc` tells it each handshake's shard in the `x-ollupi-shard` header and the load balancer holds the connections; `router.Router` takes the connections it cannot place, and anywhere without such a load balancer it takes them all. Both find the shard and its holder's address with one catalog lookup, which places a new user on the lighter of two accepting shards drawn at random, by the load each reports. The catalog is kept apart from the shards' data, behind the interface `catalog.Catalog` (`catalog`). It decides each lease by its own clock, and a holder counts its lease from before it asked and gives up a margin early, so that it stops before any other process can start. `catalog.SQL` keeps the catalog in a SQLite database of its own; a deployment that needs it to outlive any one machine implements `catalog.Catalog` over a coordination service, such as etcd, or a database served apart from the shards. A shard only checks that a connection is its user's, through `gateway.Options.Admit` set to `shard.Member.Admit`, and tells one that is not to come back later (503 with `Retry-After`). A process serves, and answers `/healthz`, only while it holds its shard, so a load balancer's health check sends the shard's connections to the process that holds it.

Everything a user runs stays in their own sandbox: commands, files and MCP servers are reached through `sandbox.Box`, and integrating a cloud sandbox only takes implementing `sandbox.Machine`. A user's environments are the boxes a conversation can be started in — this machine, and cloud sandboxes they add — and a conversation keeps the one it was started in (`environment/list`, `thread/start` with `environmentId`). HTTP MCP requests from cloud sandbox users leave from the shard process and can only reach public addresses. Model calls can go through the user's own client, and attachments are stored as files under the home's `attachments` folder (`blob.Dir`).

Two choices of the sandbox trade safety for ordinary work. A repository's `.git` is writable inside it, hooks included, so `git init` and `git clone` work there; a hook a command writes runs later for whoever runs git outside the sandbox. Under the `never` approval policy, a command that asks to leave the sandbox runs on the host without asking; a server that runs users' commands in a sandbox on its own machine should not offer that policy. `sandbox.Here` is no sandbox at all: every command it runs reaches the host under any policy, so it suits only users who may use the machine fully.

Building and testing need SQLite's FTS5: `go test -tags sqlite_fts5 ./...`, or set `GOFLAGS=-tags=sqlite_fts5`.

Clients authenticate with `Authorization: Bearer <token>`. Browsers can't set this header, so they put the token in the WebSocket subprotocol instead: `new WebSocket(url, ["ollupi.token", token])`. Tokens never go in the URL.

A user may hold up to four connections at once, one per device, and each hears all of the user's events; a fifth takes the oldest's place, which closes with 4000 and should not reconnect on its own. The gateway pings each connection every 30 seconds and drops one whose peer has not answered within 15, and closes each with 1001 at a random time between 15 and 20 hours after it opened, under the 24 hours Google Cloud's load balancer keeps a WebSocket open, so that connections opened together are not cut together. Connections are admitted at a bounded rate, so after a restart a client may be refused with 503 and a `Retry-After` in seconds. `gateway.Options.Connections` changes these bounds. What a client does after each close code and each refused handshake is part of the protocol, under `closes` and `handshakes` in `protocol.schema.json`: reconnect after a random wait of up to 10 seconds, with backoff, after `Retry-After`, or not on its own; once back, it reads its threads again, since it heard nothing meanwhile.

## License

[MIT](LICENSE)
