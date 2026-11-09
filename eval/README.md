# Eval

Measures what ollupi gets done with a real model on public benchmarks. [bench](../bench/README.md) measures scheduling and persistence with a simulated model; the two stay apart.

A score belongs to the model and the harness together: the same model scores differently in different harnesses, so a result means something only next to another harness run with the same model on the same tasks. Here the other harness is Harbor's own reference agent, terminus-2, which drives a tmux terminal with keystrokes and has no tools of its own.

## Setup, once

- Docker Desktop, with at least 4 GB for its VM.
- [Harbor](https://harborframework.com): `uv tool install --python 3.12 harbor==0.25.0`. It installs into `~/.local/bin`, which `run.sh` puts on its `PATH`.
- A model key: `OLLUPI_API_KEY` and `OLLUPI_BASE_URL` for ollupi, and the provider's own variable, such as `DEEPSEEK_API_KEY`, for Harbor's agents.

## Running

```bash
export OLLUPI_API_KEY=$DEEPSEEK_API_KEY OLLUPI_BASE_URL=https://api.deepseek.com
eval/run.sh ollupi                 # the smoke subset, ten tasks
eval/run.sh terminus-2             # the same tasks with the reference harness
eval/run.sh ollupi all --n-attempts 5
eval/run.sh ollupi -i regex-log    # one task again
python3 eval/report.py             # the comparison, at any time
```

`run.sh <agent> [subset] [harbor run flags]` takes `ollupi` or any agent Harbor ships, and a subset from [subsets](subsets) or `all` for the 89 tasks of Terminal-Bench 2.0. `MODEL` picks the model (default `deepseek/deepseek-flash`), `N` how many trials run at once (default 2; more needs more memory than Docker's 4 GB). Before it starts, it:

- downloads the tasks into `~/harbor-tasks/terminal-bench-2.0` (`TASKS_DIR`) the first time, so later runs need no clone from GitHub;
- checks that a container downloads over HTTP and HTTPS, which every verifier needs;
- for ollupi, rebuilds `eval/build` with [build.sh](build.sh) when the code changed since the last build. The amd64 build takes about ten minutes under emulation on Apple silicon.

Each trial keeps its files under `eval/jobs/<agent>/<job>/<task>__<id>/`: `agent/ollupi.txt` (ollupi's log), `agent/ollupi-items.json` (every item of the turn), `verifier/test-stdout.txt` (the tests' output) and `result.json`.

## Reading the results

[report.py](report.py) puts the agents side by side, one row per task: pass or FAIL, the agent's minutes, and T/O when it ran out of time. It counts a trial only when its verifier ran the tests (`verifier/ctrf.json` exists). A trial without it scored 0 because the tests never ran, and the report lists it to run again. For each task it takes the latest job that tested it, so running a task again replaces its earlier result.

Before changing the harness, read the failed trials' items and sort them by cause: a score alone does not say what to fix.

A result counts when:

- everything is pinned: the dataset version, the model, Harbor's version and ollupi's commit (the report lists the versions each agent ran);
- each configuration ran more than once, and the report gives the pass rate; Terminal-Bench's leaderboard takes five runs (`--n-attempts 5`) of all 89 tasks;
- another harness ran the same model on the same tasks.

The smoke subset was picked by hand, so its scores compare agents with each other, not with the leaderboard.

## When something scores 0 that should not

- **The tests never ran.** The report says "not tested"; `verifier/test-stdout.txt` shows why, usually a download that failed. GitHub's release downloads sometimes answer 504 for a while: run the task again later.
- **Docker behind a proxy.** Docker Desktop sends containers' traffic through the system proxy. A proxy that stalls on large plain-HTTP downloads breaks `apt-get` in every verifier while HTTPS works. In Docker Desktop's proxy settings, set the proxy by hand: none for HTTP, the system proxy for HTTPS.
- **The model unreachable from a task.** Some task images ship no CA certificates. The eval command carries its own roots for that case; an agent's log that shows "model request failed" on its first call points at the container's network or certificates.

## How ollupi runs a task

`eval` ([main.go](main.go)) runs one task through ollupi's agent and exits: one user, one conversation, one turn, no gateway. The task comes from `OLLUPI_INSTRUCTION`, a file or stdin. It never asks for approval, answers a question the model asks with "decide for yourself", prints the last message, writes the turn's items with `-out`, and exits 1 when the turn does not complete. Commands run with full access, as the task's container is the sandbox; on macOS, `-sandbox workspace-write` confines them to the folder:

```bash
go build -tags sqlite_fts5 -o ollupi-eval ./eval
OLLUPI_API_KEY=... OLLUPI_INSTRUCTION='Write primes.py that prints the first 20 primes and run it.' \
  ./ollupi-eval -sandbox workspace-write -base-url https://api.deepseek.com -model deepseek-flash
```

[harbor/ollupi_agent.py](harbor/ollupi_agent.py) is the Harbor agent: it uploads the static Linux build into each task's container and runs the task there, with the key and settings from the host's environment, so no key is on a command line.
