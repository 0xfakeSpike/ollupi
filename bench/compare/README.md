# Comparison with other harnesses

Measures Ollupi beside two Node.js agent harnesses that host many sessions in one process, under the same load, through the same driver, so the four can sit in one table:

- **ollupi**: this repository at the commit each result names. `bench serve` runs a shard (the host with its gateway) as a process of its own on the simulated provider; the driver reaches it as clients do, over the WebSocket protocol (`thread/start`, `turn/start`, `turn/changed`): in `parked`, `steady` and `burst` every 64 sessions belong to one user, and each user has a connection of their own; `crash` (50 sessions) and `background` (200) use one user and one connection. The gateway paces each connection at 50 messages a second, as it does any client's, so a single connection cannot carry thousands of sessions.
- **pi**: `@earendil-works/pi-coding-agent` 1.5.2 (`pi-sdk`, the SDK's `AgentSession`, one JSONL file per session), and the experimental `@earendil-works/pi-durable` 1.5.2 (`pi-durable`, one `Harness` hosting many sessions in a single SQLite file). Both run inside the driver's process.
- **dsh**: `@deepseek-ai/dsh` 0.2.2-rc.5 (DeepSeek Harness, built on the Cordis plugin framework), with the `sdk-minimal` profile: one process hosts many sessions over stdio JSON-RPC, with sessions stored as JSONL in `$DSH_HOME/sessions`.

The model is always ollupi's own simulated provider (`bench provider`, a separate process serving OpenAI-compatible streaming chat completions and returning 429 when in-flight requests exceed the quota). Unless an experiment says otherwise, each turn calls the tool `bench__work` twice (1 s each, returns "ok") and then answers with text, about 17 s when run alone. No real API key is used; `PI_OFFLINE=1` is set; `~/.pi` and `~/.dsh` are neither read nor written (all configuration lives under `run/`).

Memory is always phys_footprint (macOS `footprint`) of the process that hosts the sessions: ollupi's and dsh's own processes, and the driver's for the two pi harnesses, read after a forced GC. When the test machine is short on memory, macOS compresses pages and the RSS from `ps` reads low; phys_footprint counts compressed pages.

## What is compared, and against what

| Experiment | What it asks | Who takes part |
|---|---|---|
| `parked` | What an idle session costs in memory and disk, and how fast it comes back | All four |
| `steady` | How turn time grows with the number of sessions running at once | All four |
| `burst` | What happens to 3000 turns submitted at once against a quota of 256 | All four |
| `crash` | What happens to turns in progress when the process is killed with `kill -9`, again and again | All four; pi-sdk has no restart to resume from, so it is killed once |
| `background` | A turn starts a 20 s command in the background and answers; does its result come back to the conversation, and after a `kill -9` while the commands run? | ollupi and dsh. pi has no background calls; the control for what running in the background buys is ollupi's own foreground mode, in `bench background` ([bench](../README.md)) |
| `isolated-dsh` | One dsh process per session, the per-agent isolation baseline | dsh; ollupi's is `bench isolated` |

What the other harnesses have no counterpart of — users with their own keys on one shard, a standby that takes over a shard, a router in front of shards, client connections and their reconnect storm — is measured against ollupi with that part turned off, in [bench](../README.md) (`users`, `crash -lease`, `conns -router`).

Each harness runs as it ships, except that ollupi is given the settings a deployment would choose for the load: in `steady` its slots and model calls equal the sessions, in `burst` it has 512 slots and 256 model calls (the quota), and in `background` its background queue holds every command at once, as dsh's does. Neither pi nor dsh limits model call concurrency, so their 429s are handled by each harness's retry policy.

## Setup

```bash
cd bench/compare
npm install --ignore-scripts
(cd ../.. && go build -tags sqlite_fts5 -o bench/compare/run/bench ./bench)   # simulated provider and bench serve
```

Requires Node 22.19 or later (tested on v22.23).

## Reproducing

All experiments (results appended to `results/results.txt`):

```bash
./run-all.sh
```

One at a time (`--harness` is `ollupi`, `pi-sdk`, `pi-durable` or `dsh`):

```bash
node --expose-gc parked.mjs  --harness pi-durable --n 1000                 # memory and disk of parked sessions, restore after dropping them
node --expose-gc parked.mjs  --harness pi-durable --n 1000 --retention 0   # pi-durable keeps no idle sessions in memory
node --expose-gc parked.mjs  --harness ollupi --n 2000 --perUser 2000     # ollupi: all sessions under one user (default 64 per user)
node --expose-gc steady.mjs  --harness ollupi --c 1024                     # c sessions, 3 consecutive turns each, provider quota = c
node --expose-gc burst.mjs   --harness dsh --n 3000 --limit 256            # 3000 turns submitted at once, provider quota 256
node crash-ollupi.mjs --n 50 --turns 20 --kills 30                         # ollupi: random kill -9, restart, the client reconnects
node crash.mjs     --n 50 --turns 20 --kills 30                            # pi-durable: restart and resume after random kill -9
node crash-sdk.mjs --n 200 --turns 5 --at 1500                             # pi-sdk: session file state after one kill -9
node crash-dsh.mjs --mode closed --n 50 --turns 20 --kills 30              # dsh: random kill -9, session/resume after restart
node crash-dsh.mjs --mode queued --n 50 --turns 20 --kills 30              # dsh: what happens to pre-queued prompts after a crash
node background.mjs --harness ollupi --n 200 --seconds 20                  # 200 background commands of 20 s; does each report come back
node background.mjs --harness dsh --n 200 --seconds 20 --kill 8            # the same, with kill -9 8 s in
node isolated-dsh.mjs --k 10                                               # memory baseline for one dsh process per session
```

`locked.sh` keeps runs from overlapping with ollupi's own bench: it waits while `.locks/gobench.lock` exists and holds `.locks/compare.lock` while running.

## Files

| File | Contents |
|---|---|
| `common.mjs` | Starting the simulated provider, memory (RSS and macOS phys_footprint), percentiles, result recording |
| `harnesses.mjs` | One interface over the four harnesses |
| `ollupi/client.mjs` | ollupi: starting `bench serve` and the WebSocket JSON-RPC client |
| `pi-sdk.mjs` | pi-coding-agent: an openai-completions provider in `models.json`, the `bench__work` tool, a persistent `SessionManager` |
| `pi-durable.mjs` | pi-durable: `createProvider` pointed at the simulated provider, a `bench__work` extension, `openNodeSqliteStorage` |
| `dsh/patch.yml`, `dsh/background.yml`, `dsh/bench-tool.mjs`, `dsh/client.mjs` | dsh: a profile overlay (pi-ai adapter pointed at the simulated provider, shell tool off, `bench__work` plugin added), the overlay that adds the one-shot `bash` tool and the job tools for `background`, and the JSON-RPC client |
| `parked.mjs`, `steady.mjs`, `burst.mjs`, `crash*.mjs`, `background.mjs`, `isolated-dsh.mjs` | The experiments |
| `results/results.txt` | Raw output |

## Background calls

In `background` each of 200 sessions gets one turn. The model's first answer runs `sleep 20` in the background — ollupi's `run_command` with `background: true`, dsh's `bash` with `run_in_background: true` — and its second answers with text; when the command ends, its result comes back to the conversation, and the model answers a third time. A session finishes when that third answer ends a turn: in ollupi the turn that launched the command stays open until then, in dsh the job's completion notice wakes the idle session with a turn of its own. Both run a real `sleep` process per command. `over_seconds` is how long after the 20 s the third answer ended. With `--kill`, the process is killed with `kill -9` while the commands run and started again: ollupi's client reconnects and reads its threads again, as the protocol says; dsh's driver resumes each session with `session/resume`. Each is then given 60 s past the 20 s to finish.

## Results

2026-11-19, Ollupi v0.18.0, Apple M1 (8 cores, 8 GB), one run each. Raw output is in `results/results.txt`.

**Parked sessions.** Each session runs one turn, then sits idle.

| | Sessions | Memory per session | Disk per session | Reopen a session |
|---|---|---|---|---|
| Ollupi | 10,000 | 14 KB | 13.2 KB | 5.5 ms |
| pi (SDK) | 10,000 | 90 KB | 3.2 KB | 1.1 ms |
| pi-durable | 10,000 | 64 KB | 8.6 KB | 0.4 ms |
| DeepSeek Harness | 5,000 | 147 KB | 8.3 KB | 10.3 ms, after a 1.1 s restart |

Ollupi's 14 KB is mostly its users, not its sessions: here every 64 sessions belong to one user, and each connected user holds about 0.56 MB of heap. The same 2000 sessions take 6.1 KB each when they all belong to one user, and 22 KB each at 64 per user (`parked --perUser`); after a restart, with no user connected, the process holds 9.5 MB in all. Ollupi's disk per session is the largest, since every step of the turn is kept in its log.

**Sessions running at once.** Each session runs 3 turns back to back; the quota equals the sessions. Turn p50 over the 17 s of running alone, and memory at peak:

| Sessions | Ollupi | pi (SDK) | pi-durable | DeepSeek Harness |
|---|---|---|---|---|
| 64 | +0%, 36 MB | +0%, 135 MB | −1%, 111 MB | +1%, 228 MB |
| 256 | +1%, 79 MB | +2%, 164 MB | +7%, 245 MB | +9%, 295 MB |
| 1024 | +15%, 250 MB | +1%, 307 MB | +11%, 490 MB | +38%, 980 MB |
| 2048 | +76%, 398 MB | +28%, 777 MB | +58%, 925 MB | +330%, 1371 MB |

No turn failed. Ollupi takes the least memory at every size, and from 1024 sessions its turns slow down more than pi's: every step is written before it runs, on the store's one SQLite connection, and the turns wait for it (see [bench](../README.md#capacity)). Disk per turn: Ollupi 13.1 KB, pi (SDK) 2.6 KB, pi-durable 6.3 KB, DeepSeek Harness 6.8 KB.

**3000 turns submitted at once, quota 256.**

| | Completed | Failed | 429s | Duration | Memory at peak |
|---|---|---|---|---|---|
| Ollupi | 3000 | 0 | 0 | 181 s | 138 MB |
| pi (SDK) | 483 | 2517 | 11,130 | 44 s | 1199 MB |
| pi-durable | 527 | 2473 | 11,256 | 47 s | 875 MB |
| DeepSeek Harness | 1764 | 1236 | 16,749 | 197 s | 1453 MB |

The quota allows 176 s for all 3000. Ollupi holds the requests beyond it in its own queue; the others send them all and retry (both pi harnesses 3 times with exponential backoff from 2 s, DeepSeek Harness 5 times from 500 ms to 10 s), and a turn whose retries run out ends with an error.

**`kill -9` mid-run.** Model calls 200 ms, tool calls 300 ms, killed after a random 0.3–2 s.

| | Load | Kills | Turns | Tool calls run twice | Restart |
|---|---|---|---|---|---|
| Ollupi | 50 sessions × 20 turns | 30 | All 1000 complete | 0 | 32 ms |
| pi-durable | 50 sessions × 20 turns | 30 | All 1000 complete; 357 interrupted tool calls go back to the model as errors | 0 | 184 ms |
| pi (SDK) | 200 sessions × 5 turns | 1 | The 200 turns in progress stop and do not go on when reopened; new prompts run | 0 | — |
| DeepSeek Harness | 50 sessions × 20 turns, sent one by one | 20 | 980 end interrupted and 11 complete; 19 prompts it acknowledged are lost | 0 | 1.2 s |
| DeepSeek Harness | 50 sessions × 20 turns, queued at once | 1 | The 915 queued prompts are cancelled on resume | 0 | 1.3 s |

**Background calls.** 200 sessions each run `sleep 20` in the background.

| | Results back | With `kill -9` while the commands run | Memory at peak |
|---|---|---|---|
| Ollupi | 200 of 200, 3.9 s after the command at p50 | 200 of 200, each reporting that it was cut off | 82 MB |
| DeepSeek Harness | 200 of 200, 4.8 s after the command at p50 | 0 of 200 | 281 MB |
| pi (SDK), pi-durable | No background calls | — | — |

`reports_cut_off=199` in the raw output undercounts: it was read back through the protocol, and the run's database holds 200 cut-off reports, one in each session.

**One process per session.** 10 DeepSeek Harness processes take 129 MB each; one Ollupi process per session takes 17.7 MB (`bench isolated`).
