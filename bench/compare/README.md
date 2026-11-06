# Comparison with other harnesses

Measures two Node.js agent harnesses under the same load as ollupi's `bench`, so the results can sit next to the numbers in [bench](../README.md):

- **pi**: `@earendil-works/pi-coding-agent` 1.4.0 (`pi-sdk`, the SDK's `AgentSession`, one JSONL file per session), and the experimental `@earendil-works/pi-durable` 1.4.0 (`pi-durable`, one `Harness` hosting many sessions in a single SQLite file).
- **dsh**: `@deepseek-ai/dsh` 0.2.1-rc.5 (DeepSeek Harness, built on the Cordis plugin framework). Uses the `sdk-minimal` profile: one process hosts many sessions over stdio JSON-RPC, with sessions stored as JSONL in `$DSH_HOME/sessions`.

The model is always ollupi's own simulated provider (`bench provider`, a separate process serving OpenAI-compatible streaming chat completions and returning 429 when in-flight requests exceed the quota). Each turn calls the tool `bench__work` twice (1 s each, returns "ok") and then answers with text, about 17 s when run alone. No real API key is used; `PI_OFFLINE=1` is set; `~/.pi` and `~/.dsh` are neither read nor written (all configuration lives under `run/`).

## Setup

```bash
cd bench/compare
npm install --ignore-scripts
(cd ../.. && go build -tags sqlite_fts5 -o bench/compare/run/bench ./bench)   # simulated provider
```

Requires Node 22.19 or later (tested on v22.23).

## Reproducing

All experiments (about an hour, results appended to `results/results.txt`):

```bash
./run-all.sh
```

One at a time (`--harness` is `pi-sdk`, `pi-durable` or `dsh`):

```bash
node --expose-gc parked.mjs  --harness pi-durable --n 1000                 # 1. memory and disk of parked sessions, time to restore from disk after dropping them
node --expose-gc parked.mjs  --harness pi-durable --n 1000 --retention 0   #    pi-durable keeps no idle sessions in memory
node --expose-gc steady.mjs  --harness pi-sdk --c 1024                     # 2. c sessions, 3 consecutive turns each, provider quota = c
node --expose-gc burst.mjs   --harness dsh --n 3000 --limit 256            # 3. 3000 turns submitted at once, provider quota 256
node crash.mjs     --n 50 --turns 20 --kills 30                            # 4. pi-durable: restart and resume after random kill -9
node crash-sdk.mjs --n 200 --turns 5 --at 1500                             #    pi-sdk: session file state after one kill -9
node crash-dsh.mjs --mode closed --n 50 --turns 20 --kills 30              #    dsh: random kill -9, session/resume after restart
node crash-dsh.mjs --mode queued --n 50 --turns 20 --kills 30              #    dsh: what happens to pre-queued prompts after a crash
node isolated-dsh.mjs --k 10                                               # 5. memory baseline for one dsh process per session
```

`locked.sh` keeps runs from overlapping with ollupi's own bench: it waits while `.locks/gobench.lock` exists and holds `.locks/compare.lock` while running.

## Files

| File | Contents |
|---|---|
| `common.mjs` | Starting the simulated provider, memory (RSS and macOS phys_footprint), percentiles, result recording |
| `harnesses.mjs` | One interface over the three harnesses |
| `pi-sdk.mjs` | pi-coding-agent: an openai-completions provider in `models.json`, the `bench__work` tool, a persistent `SessionManager` |
| `pi-durable.mjs` | pi-durable: `createProvider` pointed at the simulated provider, a `bench__work` extension, `openNodeSqliteStorage` |
| `dsh/patch.yml`, `dsh/bench-tool.mjs`, `dsh/client.mjs` | dsh: a profile overlay (pi-ai adapter pointed at the simulated provider, shell tool off, `bench__work` plugin added) and the JSON-RPC client |
| `parked.mjs`, `steady.mjs`, `burst.mjs`, `crash*.mjs`, `isolated-dsh.mjs` | The experiments |
| `results/results.txt` | Raw output |

## Notes

- Both pi harnesses run inside the test process, and memory is read after a forced GC; dsh is a separate process, read with `ps` and `footprint`.
- When the test machine is short on memory, macOS compresses pages and the RSS from `ps` reads low; `footprint` (phys_footprint) counts compressed pages, so it is the number to trust.
- Neither pi nor dsh limits model call concurrency, so 429s in the burst test are handled by each harness's retry policy.

## Results (2026-11-02, Apple M1, 8 cores, 8 GB, one run each)

Raw output is in `results/results.txt`. The first dsh burst result in `results/run-all.log` (`failed_turns=0`) is wrong: the client then treated the return of `session/wait` as success, but dsh returns however the turn ends. After fixing the client to read the `reason` from `turn/end`, the rerun is in `results/extra.log`. The dsh closed-loop runs each made exactly 3 times as many requests as turns with no 429s, so they are unaffected. Memory is always phys_footprint.

**1. Parked sessions** (each session runs one turn, then sits idle)

| | Sessions | Memory/session | Disk/session | Can idle sessions leave memory | Restore from disk |
|---|---|---|---|---|---|
| pi-sdk | 10000 | 91 KB (JS heap 25 KB) | 3.2 KB (JSONL) | Yes, reopen from the file after `dispose()` | 0.8 ms |
| pi-durable | 10000 | 23 KB (JS heap 12.5 KB, kept in memory for 10 minutes by default) | 8.5 KB (SQLite) | Handles are stateless and released after the retention period | 0.4 ms (reopening the Harness: 6 ms) |
| dsh | 5000 | 181 KB | 8.3 KB (JSONL) | The SDK protocol has no method to close a session; only a process restart does | Process restart 0.9 s, `session/resume` 9.6 ms |
| ollupi | 50000 | ≈0.3 KB | 2.3 KB | Not held in memory | — |

pi-durable took 551 s to create 10,000 sessions but only 14 s for 1000; throughput drops as the session count grows.

**2. Closed-loop load** (3 consecutive turns per session, quota = c; the table shows the increase in turn p50 over the 17 s of running alone)

| c | pi-sdk | pi-durable | dsh | ollupi |
|---|---|---|---|---|
| 64 | +1% | −1% | +2% | |
| 256 | +1% | +1% | +2% | |
| 1024 | +1% | +19% | +15% | |
| 1536 | | | | +2% |
| 2048 | +2% (p99 22 s) | +314% (p50 70 s) | +77% (p50 30 s) | +2% |

Peak memory at c = 2048: pi-sdk 604 MB (230 KB/session), pi-durable 733 MB (318 KB/session), dsh 1414 MB (659 KB/session); an active ollupi session takes about 160 KB. CPU: pi-sdk 0.46 cores, pi-durable 1.00 cores, dsh 0.86 cores (Node is single-threaded, so close to 1 core means saturated). Disk per turn: pi-sdk 2.6 KB, pi-durable 6.2 KB (after closing; the WAL is larger while running), dsh 6.8 KB.

**3. Burst** (3000 turns submitted at once, quota 256, theoretical floor 176 s)

| | Completed | Failed | 429s | Duration |
|---|---|---|---|---|
| pi-sdk | 466 | 2534 | 10953 | 39 s |
| pi-durable | 819 | 2181 | 10680 | 98 s |
| dsh | 810 | 2190 | 19537 | 93 s |
| ollupi | 3000 | 0 | 0 | 1.03 times the floor |

None of the three limits model call concurrency; they only retry (both pi harnesses default to 3 attempts with exponential backoff from 2 s; dsh defaults to 5 attempts from 500 ms to 10 s), and turns that run out of retries end with an error.

**4. Crash** (200 ms model, 300 ms tools, `kill -9` after a random 0.3–2 s)

| | Load | Kills | Turn outcome | Tools run twice | Restart |
|---|---|---|---|---|---|
| pi-durable | 50 sessions × 20 queued turns | 27 | All 1000 complete; 683 interrupted tool calls go to the model as interrupted errors and are not rerun | 0 | 212 ms (p50) |
| pi-sdk | 200 sessions × 5 turns | 1 | The 200 in-progress turns stop halfway and don't continue after reopening; new prompts run normally | 0 (not rerun) | — |
| dsh (sent one by one) | 50 sessions × 20 turns | 18 | 874 end as interrupted, 113 complete; interrupted turns don't continue on resume | 0 by call ID, but 228 of 1247 tool executions have no matching call in the session log | 1.0 s (including 50 `session/resume`) |
| dsh (pre-queued) | 50 sessions × 20 turns | 1 | 900 queued prompts are cancelled on resume | Same as above | 0.9 s |
| ollupi | 960 turns | 90 | All complete; interrupted calls are marked as possibly applied | 0 | 448 ms (10,000 parked + 500 running) |

**5. One dsh process per session**: 10 processes average 107 MB per process (ollupi: 20 MB per process).
