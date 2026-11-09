# Eval

Measures what ollupi gets done with a real model on public benchmarks, to find what to improve and to tell whether a change helped. [bench](../bench/README.md) measures scheduling and persistence with a simulated model; the two stay apart.

The tasks are Terminal-Bench 2.0's, run in Docker by [Harbor](https://harborframework.com). ollupi is compared with its own earlier versions; how other harnesses do comes from the published leaderboards, not from running them here (see the end).

## Setup, once

- Docker Desktop, with at least 4 GB for its VM.
- Harbor: `uv tool install --python 3.12 harbor==0.25.0`. It installs into `~/.local/bin`, which the scripts put on their `PATH`.
- The model's key and endpoint: `OLLUPI_API_KEY` and `OLLUPI_BASE_URL` (and `OLLUPI_API` for an API other than OpenAI's; see [harbor/ollupi_agent.py](harbor/ollupi_agent.py)).

## The loop

```bash
export OLLUPI_API_KEY=$DEEPSEEK_API_KEY OLLUPI_BASE_URL=https://api.deepseek.com
eval/run.sh ollupi                                   # 1. run the smoke tasks
python3 eval/report.py                               # 2. which passed, failed and took long
python3 eval/trace.py eval/jobs/ollupi/<job>/<trial> # 3. what happened in one of them
# 4. change ollupi
eval/run.sh ollupi -i extract-elf --n-attempts 3     # 5. the tasks the change is for, more than once
eval/run.sh ollupi                                   # 6. all of smoke, for what it broke
python3 eval/report.py                               #    the change against the version before it
eval/run.sh ollupi holdout                           # 7. once it looks right, the tasks it was not shaped on
```

1. **Run.** `run.sh <agent> [subset] [harbor run flags]` takes a subset from [subsets](subsets) (default `smoke`) or `all` for the 89 tasks. Before it starts it checks that Docker runs and that a container downloads over HTTP and HTTPS, downloads the tasks into `~/harbor-tasks/terminal-bench-2.0` (`TASKS_DIR`) the first time, and rebuilds `eval/build` when the code changed since the last build. The amd64 build takes about ten minutes under emulation on Apple silicon. `MODEL` picks the model (default `deepseek/deepseek-flash`), `N` how many trials run at once (default 2).
2. **Compare.** [report.py](report.py) puts ollupi's latest version next to what came before: for each task, the latest result of any earlier version, tagged with the commit it came from, so running only the tasks a change is for still compares each of them. It says which tasks the change fixed or broke, and how the tasks both tested compare; `--all` gives every version a column. A version is the commit eval was built from, with a `-dirty+` hash when changes were not committed, so that two experiments on one commit stay apart. A trial counts only when its verifier ran the tests; the others are listed to run again. For each task the latest job that tested it gives the result, so running a task again replaces it.
3. **Read.** [trace.py](trace.py) shows a trial: the failing tests and their assertions, then the turn as a timeline of model responses (with what the model thought and said) and tool calls (with arguments, time, exit codes), and where the time went. eval keeps `agent/ollupi-items.json` up to date as the turn goes, so a run the harness stopped at its time limit shows up to that point. Sort each failure by cause before changing anything:
   - the **model** could not do it: a better model would, the harness would not;
   - the **harness**: a tool that misled, a prompt that pushed the wrong way, context lost, not knowing when to stop. These are the ones to fix;
   - the **environment**: the network or the image, nothing to do with ollupi.
4. **Change** one thing at a time, in a way that helps tasks in general. A fix for one task's quirk teaches nothing.
5. **Check the change** on the tasks it is for, with `--n-attempts 3` or more: one trial of one task passes or fails by chance often enough to mislead.
6. **Check what it broke** on all of smoke.
7. **Confirm** on `holdout`. Run it only once a change looks right on smoke, and do not read its trials to steer the next change: it tells whether a change helps tasks it was not shaped on only as long as it was not shaped on them.

Iterate with a cheap model, and confirm with the model ollupi will run on: what helps one model does not always help another.

## Subsets

- [smoke](subsets/smoke.txt): ten tasks, two easy, seven medium, one hard. For iterating; read its trials freely.
- [holdout](subsets/holdout.txt): twenty other tasks, one easy, sixteen medium, three hard. For confirming.
- `all`: the 89 tasks, for a result to state in public.

Both subsets were picked by hand, each task needing 2 GB and none a virtual machine or a large model, so their scores compare versions of ollupi, not ollupi with the leaderboard.

## Against other harnesses

The leaderboards on Harbor Hub carry the published scores of many harnesses with many models:

```bash
harbor hub leaderboard show terminal-bench/terminal-bench-2/2-0
```

A score of ollupi's sits next to them only when it was made the way theirs were: a model that is on the board, all 89 tasks, five attempts each (`--n-attempts 5`), default time limits and resources, and an environment as stable as theirs; a cloud one such as Daytona or Modal rather than a laptop behind a proxy. The board publishes overall scores only, not each task's trials.

## When something scores 0 that should not

- **The tests never ran.** The report lists it; `trace.py` shows the end of the verifier's output, usually a download that failed. GitHub's release downloads sometimes answer 504 for a while: run the task again later.
- **Docker behind a proxy.** Docker Desktop sends containers' traffic through the system proxy. A proxy that stalls on large plain-HTTP downloads breaks `apt-get` in every verifier while HTTPS works. In Docker Desktop's proxy settings, set the proxy by hand: none for HTTP, the system proxy for HTTPS. A model provider that is faster reached directly can be added to the proxy's exclusions.
- **The model unreachable from a task.** Some task images ship no CA certificates. eval carries its own roots for that case; "model request failed" on the first call points at the container's network or certificates.

## How ollupi runs a task

`eval` ([main.go](main.go)) runs one task through ollupi's agent and exits: one user, one conversation, one turn, no gateway. The task comes from `OLLUPI_INSTRUCTION`, a file or stdin. It never asks for approval, answers a question the model asks with "decide for yourself", prints the last message, keeps the turn's items in `-out`, and exits 1 when the turn does not complete. Commands run with full access, as the task's container is the sandbox; on macOS, `-sandbox workspace-write` confines them to the folder:

```bash
go build -tags sqlite_fts5 -o ollupi-eval ./eval
OLLUPI_API_KEY=... OLLUPI_INSTRUCTION='Write primes.py that prints the first 20 primes and run it.' \
  ./ollupi-eval -sandbox workspace-write -base-url https://api.deepseek.com -model deepseek-flash
```

[harbor/ollupi_agent.py](harbor/ollupi_agent.py) is the Harbor agent: it uploads the static Linux build into each task's container and runs the task there, with the key and settings from the host's environment, so no key is on a command line. Each trial keeps `agent/ollupi.txt` (the log), `agent/ollupi-items.json` (the turn), `verifier/test-stdout.txt` and `result.json` under `eval/jobs/ollupi/<job>/<task>__<id>/`.
