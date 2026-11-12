# Bench

Measures Ollupi's scheduling and persistence, not model quality. The model and tools are simulated: the simulated provider is reached through the OpenAI client and returns 429 when in-flight requests exceed the quota. Unless noted otherwise, a model call takes 5 s ± 2 s, a tool call takes 1 s, and each turn makes 3 model calls and 2 tool calls, about 17 s when run alone. The test machine is an Apple M1 (8 cores, 8 GB) with a single SQLite database.

The README figures are drawn by [docs/images/figures.py](../docs/images/figures.py). Raw output is kept in [results](results), and the comparison with other harnesses is in [compare](compare/README.md).

## Running

Nothing else should be running on the machine during a measurement, or the latency numbers can't be trusted:

- Run one experiment at a time; don't overlap different experiments or different projects.
- Quit VMs such as Docker Desktop first, make sure no other project is running tests, and check that the CPU is at least 60% idle.
- Keep the lid open. Closing it puts the system to sleep, and `caffeinate` won't stop that; discard any result from a run that slept.

[run.sh](run.sh) runs the experiments below one at a time and holds to these rules: before each it waits until Docker Desktop is not running, the lid is open, the CPU is at least 60% idle and no [comparison](compare/README.md) is running, and a run the Mac slept through is thrown away and run again. It keeps each result as `results/<experiment>.txt`, headed by the command, the commit, the time and the machine. `bench/run.sh -l` lists the experiments, and `bench/run.sh steady-1024 crash` runs only those.

```bash
bench/run.sh
go run -tags sqlite_fts5 ./bench parked -n 50000 -state waiting -latency 5s -rounds 3
go run -tags sqlite_fts5 ./bench isolated -k 20
go run -tags sqlite_fts5 ./bench steady -c 3072
go run -tags sqlite_fts5 ./bench steady -c 1024 -fail 0.05 -others 256
go run -tags sqlite_fts5 ./bench steady -c 1536 -sigma 0.6
go run -tags sqlite_fts5 ./bench steady -c 1024 -command workspace-write
go run -tags sqlite_fts5 ./bench steady -c 1024 -command full-access
go run -tags sqlite_fts5 ./bench burst -n 3000 -slots 512 -calls 256 -latency 5s -jitter 2s -tool 1s
go run -tags sqlite_fts5 ./bench resume
go run -tags sqlite_fts5 ./bench crash -n 500 -kills 30
go run -tags sqlite_fts5 ./bench crash -n 400 -kills 8 -lease 1500ms
go run -tags sqlite_fts5 ./bench mix
go run -tags sqlite_fts5 ./bench fanout
go run -tags sqlite_fts5 ./bench soak -c 256 -d 8m -every 30s -latency 1s -jitter 200ms -tool 200ms
go run -tags sqlite_fts5 ./bench users
go run -tags sqlite_fts5 ./bench users -user-slots 64
go run -tags sqlite_fts5 ./bench users -user-slots 64 -heavy-calls 64 -heavy-limit 32
```

| Experiment | What it measures | Raw output |
|---|---|---|
| `parked` | Memory and disk of parked sessions, and their effect on active turns | `results/parked.txt`, not yet measured with run.sh |
| `isolated` | Control: one process per session | `results/isolated.txt`, not yet measured with run.sh |
| `steady` | Capacity under closed-loop load; `-fail`, `-others` and `-sigma` inject faults and a long tail; `-command` runs each tool call as a real `sleep` through `run_command` in a sandbox of that mode | `results/steady-*.txt`; `steady-workspace-write.txt` and `steady-full-access.txt` not yet measured |
| `burst` | Queueing and quota when many turns are submitted at once | `results/burst.csv` (timeline) |
| `resume` | Time from waking an idle session to its model request going out | `results/resume.txt`, not yet measured with run.sh |
| `crash` | Recovery after random `kill -9`; with `-lease`, a standby process takes over | `results/crash.txt` and `crash-lease.txt`, not yet measured with run.sh |
| `mix` | Waiting and fairness when user turns, subagents and scheduled tasks mix | `results/mix.txt` |
| `fanout` | Whether parent turns give up their slots when subagents outnumber slots | `results/fanout.txt` |
| `soak` | Latency, memory, goroutines, GC and database growth over a long run | `results/soak.csv` |
| `users` | Many users on one shard, each on their own key and quota, beside one user who keeps more conversations busy than their key serves; with `-user-slots`, whether `UserSlots` keeps the others' turns from waiting on that user, and with `-heavy-limit`, whether that user's 429s stay theirs | `results/users.txt`, `users-slots.txt` and `users-limit.txt`, not yet measured |

`go test -tags sqlite_fts5 ./bench` runs every experiment as its own process at a size that ends in seconds and checks what it exists to show: turns end, none fail, no tool call runs twice, and the users beside a heavy one draw no 429. It measures nothing; `-short` skips it.

The numbers in `results` were measured on 2026-11-04, one run each. `parked`, `isolated`, `burst`, `resume` and `crash` are earlier results that have not been re-measured since. All of these numbers predate the multi-user, shard and sandbox interface changes and have not been re-measured after them.

## Results

### Memory

Parked sessions don't stay in memory: 50,000 parked sessions add about 14 MB of resident memory (including the test program's own records), the database takes about 2.3 KB per session, and active turns in the same process are unaffected (p50 goes from 5.04 s to 5.05 s). An active session takes about 160 KB. The control, one Ollupi process per session, takes 20.0 MB per session, not counting container overhead.

### Capacity

| Active sessions | p50 | p95 | p99 | Model calls/s | CPU (cores) | Resident memory | Connection wait/turn |
|---|---|---|---|---|---|---|---|
| 512 | 17.1 s | 20.4 s | 21.4 s | 72 | 0.39 | 132 MB | 2.9 ms |
| 1024 | 17.2 s | 20.5 s | 21.6 s | 147 | 0.60 | 223 MB | 4.5 ms |
| 1536 | 17.3 s | 20.7 s | 21.9 s | 219 | 0.81 | 315 MB | 15.2 ms |
| 2048 | 17.3 s | 20.8 s | 22.1 s | 296 | 1.03 | 407 MB | 6.3 ms |
| 3072 | 18.0 s | 22.5 s | 24.0 s | 416 | 1.39 | 570 MB | 22.8 ms |
| 4096 | 20.9 s | 26.1 s | 27.7 s | 477 | 1.67 | 692 MB | 43.0 ms |

Each turn does 68 reads and 15 writes and takes about 11.6 KB of disk. At 4096 sessions a turn is 3.9 s slower than when run alone, yet waiting for SQLite connections adds up to only 43 ms; the bottleneck has not been found.

### Burst

With 3000 turns submitted at once, in-flight requests never exceed the quota, no 429 occurs, and the run finishes in 1.03 times the theoretical floor.

### Recovery

Under mixed load (tool calls, questions to the user, spawned subagents), 90 random `kill -9`s in total: all 960 turns complete, no tool call runs twice, and every interrupted call is marked as possibly applied. So that interruptions land in every execution phase, this test shortens model calls to 200 ms and tool calls to 300 ms. With 10,000 parked sessions and 500 running turns, a restart takes 448 ms. The time from waking an idle session to its model request going out grows with history length; p50 is 73 ms at 1000 turns of history.

### Mixed load

256 sessions send turns continuously (20% of them handed to subagents) while 1000 scheduled tasks come due at once mid-run. Off-peak, the wait for a turn's first model request has a p50 of 7 ms and a p99 of 274 ms. Turns arriving during the peak queue with the scheduled tasks in ready order, with a p50 of 44.1 s, and the peak drains in 59.4 s, about the floor the model quota allows. Subagents wait 4.6 s at p99, and the Jain fairness index of turn time across sessions is 0.978.

### Subagent fan-out

64 turns each spawn 8 subagents at once, 512 subagents on only 32 slots: all 576 turns complete in 1.11 times the theoretical floor, and every parent turn waiting on subagents gives up its slot.

### Faults and long tail

With 5% of model requests returning 500 and other clients on the same account taking up to 256 of the quota, none of the 3072 turns from 1024 sessions fail, and p99 rises from 21.6 s to 28.5 s. Before backoff retries were added, the same load failed 326 turns (raw output of that run was not kept). With model latency changed to a log-normal distribution (median 5 s, σ = 0.6), none of the 4608 turns from 1536 sessions fail, with a p50 of 19.0 s and a p99 of 42.4 s.

### Long run

256 sessions run for 8 minutes (1 s model calls, 35,000 turns in all): goroutines hold at about 1000, GC pause p99 stays within 0.23 ms, the WAL holds at 6.1 MB, the database grows about 3.2 KB per turn, and turn p50 goes from 3.44 s to 3.60 s. CPU and memory grow with session history: as history goes from 8 to 138 turns, CPU per turn rises from 12.1 ms to 34.8 ms, mostly spent encoding requests that carry the full history and re-reading the history each turn, and resident memory rises from 99 MB to 396 MB.

### Comparison with other harnesses

Same simulated provider and load, one run each. Memory is phys_footprint, except Ollupi's, which is resident memory.

| | Ollupi | pi (SDK) | pi-durable | DeepSeek Harness |
|---|---|---|---|---|
| Memory per parked session | About 0.3 KB | 91 KB | 23 KB | 181 KB |
| p50 increase at 2048 active sessions | 2% | 2% | 314% | 77% |
| 3000 turns submitted at once, quota 256 | All complete | 2534 failed | 2181 failed | 2190 failed |
| After `kill -9` mid-run | All resume, no tool runs twice | In-progress turns lost | All resume, no tool runs twice | Turns marked interrupted |

Versions are pi-coding-agent 1.4.0, pi-durable 1.4.0 (experimental) and DeepSeek Harness 0.2.1-rc.5. Method and raw results are in [compare](compare/README.md).
