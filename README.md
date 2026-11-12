<h1>
  <img alt="Ollupi" src="docs/logo/logo.svg" width="240">
</h1>

Ollupi is a Go agent framework for cloud providers building massively concurrent personal agent services.

Instead of isolating each agent in its own VM or container, Ollupi pools the runtime and persists each agent's run state:

- **Recoverable at any moment.** Every step is written to the database before it runs, and no scheduling state lives in memory. After a release, restart or crash, sessions continue from where they stopped.
- **Inactive sessions cost nothing.** A session waiting on a user, an approval or a subagent leaves the execution pool and remains only as database records, using no memory or compute.
- **Controlled concurrency.** The execution pool schedules by slot, and model calls are limited per connection to match the provider's account quota; bursts queue.

Sessions, subagents and scheduled tasks all share one execution pool. [ollupi-desktop](https://github.com/0xfakeSpike/ollupi-desktop) is the desktop client built on Ollupi.

![Ollupi architecture](docs/images/architecture.svg)

## How it differs from per-agent isolation

| | One VM or container per agent | Ollupi |
|---|---|---|
| One process hosts | One session | All sessions |
| Inactive sessions | Process stays up, or is rebuilt with a cold start | Database records only |
| Waiting on a user or approval | Process sits suspended, still holding memory | Leaves the execution pool, using no resources |
| Scheduling state | Process memory | SQLite |
| After a restart | In-progress work is interrupted | Continues from where it stopped |
| Subagents | Special mechanism or child process | Ordinary sessions, queued in the same pool |
| Scheduled tasks | External cron starts a process | A message to the session when due, queued in the same pool |

![Per-agent isolation compared with Ollupi](docs/images/cli-vs-ollupi.svg)

## Benchmarks

These results use a simulated model and tools. They measure scheduling and persistence, not model quality. See [bench](bench/README.md) for the method, full data and commands to reproduce them.

![Comparison with pi and DeepSeek Harness](docs/images/bench-compare.svg)

![Memory](docs/images/bench-memory.svg)

![Capacity](docs/images/bench-capacity.svg)

![Burst and quota](docs/images/bench-burst.svg)

![Recovery](docs/images/bench-recovery.svg)

![Mixed load](docs/images/bench-mix.svg)

![Faults and long tail](docs/images/bench-faults.svg)

![Long run](docs/images/bench-soak.svg)

## Execution model

![Shared execution pool](docs/images/execution-pool.svg)

Every runnable turn in every session, whether it comes from a user, a subagent or a scheduled task, enters one queue and runs on a fixed number of slots (32 by default). A turn waiting on a user, an approval or a subagent releases its slot, holds no goroutine or model concurrency, and becomes ready again when the wait ends. When slots run short, ordinary sessions go before subagents, and turns of the same kind run in the order they became ready. Model concurrency is limited per connection (4 per connection by default), and tool execution uses none of it. Model requests that hit a 429, a 5xx or a dropped connection retry with backoff, up to 8 times, keeping their slot and model concurrency while they wait.

Every step is written to the database before it runs and no scheduling state lives in memory, so after a process restart the same scheduling logic recovers in-progress turns. Tool calls distinguish `pending` (not yet run) from `inProgress` (may have taken effect): on recovery the former run as usual, while the latter are marked failed with a prompt to verify, and are never run twice.

## Tracing

Ollupi traces through OpenTelemetry's global `TracerProvider`, so the host picks the exporter and the sampler with `otel.SetTracerProvider`; until it does, tracing costs nothing. Each run of a turn in a slot is an `ollupi.turn.run` span, preceded by an `ollupi.turn.wait` span from when the turn became ready, with the model calls (`chat {model}`, and one HTTP span per try of a retried request), tool calls (`execute_tool {tool}`), context compaction and sandboxed commands under it. All the runs of a turn, in whatever process they ran, share one trace whose ID is the turn's ID without its dashes, so a sampler that decides on the trace ID, such as `TraceIDRatioBased`, keeps or drops a turn whole. Each client request is a span of its own named after its method, and one that starts or steers a turn links to the turn's trace. No trace context is sent to model providers, and no message, tool output or command line goes into a span.

## Multi-user and shards

One database is one shard. Users in a shard share tables and are isolated per user. A lease ensures that only one process runs a shard at a time; when that process exits, a standby takes over and continues from where it stopped. The number of shards sets capacity. Clients reach one address and never learn which shard they are on: where a load balancer can call out over Envoy's external processing protocol, as Google Cloud's Application Load Balancer does with a route extension, `router/extproc` tells it each handshake's shard in the `x-ollupi-shard` header and the load balancer holds the connections; `router.Router` takes the connections it cannot place, and anywhere without such a load balancer it takes them all. Both find the shard with one `router.Locator`, which places new users and remembers where each user is. Which shard each user is on is kept apart from the shards' data: `router.Catalog` keeps it in a SQLite database of its own and places each new user on the lighter of two accepting shards drawn at random, by the load each shard reports: `gateway.Options.Load` has the shard's gateway report it every so often, counting users active of late (the default), turns under way or connections, as the deployment chooses, and a deployment can keep it in another database, Redis or a service instead. A shard only checks that a connection is its user's, through `gateway.Options.Placements`, and tells one that is not to come back later (503 with `Retry-After`). Each shard answers `/healthz` while it serves, and it serves only once it holds its lease, so a load balancer's health check sends the shard's connections to the process that holds it.

Everything a user runs stays in their own sandbox: commands, files and MCP servers are reached through `sandbox.Box`, and integrating a cloud sandbox only takes implementing `sandbox.Machine`. A user's environments are the boxes a conversation can be started in — this machine, and cloud sandboxes they add — and a conversation keeps the one it was started in (`environment/list`, `thread/start` with `environmentId`). HTTP MCP requests from cloud sandbox users leave from the shard process and can only reach public addresses. Model calls can go through the user's own client, and attachments are stored through `blobs.Store`.

Two choices of the sandbox trade safety for ordinary work. A repository's `.git` is writable inside it, hooks included, so `git init` and `git clone` work there; a hook a command writes runs later for whoever runs git outside the sandbox. Under the `never` approval policy, a command that asks to leave the sandbox runs on the host without asking; a server that runs users' commands with `sandbox.Here` should not offer that policy.

Building and testing need SQLite's FTS5: `go test -tags sqlite_fts5 ./...`, or set `GOFLAGS=-tags=sqlite_fts5`.

Clients authenticate with `Authorization: Bearer <token>`. Browsers can't set this header, so they put the token in the WebSocket subprotocol instead: `new WebSocket(url, ["ollupi.token", token])`. Tokens never go in the URL.

A user may hold up to four connections at once, one per device, and each hears all of the user's events; a fifth takes the oldest's place, which closes with 4000 and should not reconnect on its own. The gateway pings each connection every 30 seconds and drops one whose peer has not answered within 15, and closes each with 1001 at a random time between 15 and 20 hours after it opened, under the 24 hours Google Cloud's load balancer keeps a WebSocket open, so that connections opened together are not cut together. Connections are admitted at a bounded rate, so after a restart a client may be refused with 503 and a `Retry-After` in seconds. `gateway.Options.Connections` changes these bounds. What a client does after each close code and each refused handshake is part of the protocol, under `closes` and `handshakes` in `protocol.schema.json`: reconnect after a random wait of up to 10 seconds, with backoff, after `Retry-After`, or not on its own; once back, it reads its threads again, since it heard nothing meanwhile.

## License

[MIT](LICENSE)
