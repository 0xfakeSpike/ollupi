# Eval

Measures what ollupi gets done with a real model on public benchmarks, to find what to improve and to tell whether a change helped. [bench](../bench/README.md) measures scheduling and persistence with a simulated model; the two stay apart.

The tasks are Terminal-Bench 2.0's, run in Docker by [Harbor](https://harborframework.com). ollupi is compared with its own earlier versions; how other harnesses do comes from the published leaderboards, not from running them here (see the end).

## Setup, once

- Docker Desktop, with at least 4 GB for its VM.
- Harbor: `uv tool install --python 3.12 harbor==0.25.0`. It installs into `~/.local/bin`, which the scripts put on their `PATH`.
- The model, from the host's environment, which the Harbor agent passes into each task's container (never on a command line):

  | Variable | |
  |---|---|
  | `OLLUPI_API_KEY` | the provider's key (required) |
  | `OLLUPI_BASE_URL` | the provider's base URL (default: the API's own) |
  | `OLLUPI_API` | `openai` (default, also for compatible providers), `anthropic` or `gemini` |
  | `OLLUPI_CONTEXT` | the model's context window in tokens (default 128000) |
  | `OLLUPI_OUTPUT` | the most tokens one response writes (default 8192) |

## The loop

```bash
export OLLUPI_API_KEY=$DEEPSEEK_API_KEY OLLUPI_BASE_URL=https://api.deepseek.com
eval/run.sh                                   # 1. run the smoke tasks
python3 eval/report.py                        # 2. which passed, failed and took long
python3 eval/trace.py eval/jobs/<job>/<trial> # 3. what happened in one of them
# 4. change ollupi
eval/run.sh -i extract-elf --n-attempts 3     # 5. the tasks the change is for, more than once
eval/run.sh                                   # 6. all of smoke, for what it broke
python3 eval/report.py                        #    the change against the version before it
eval/run.sh holdout                           # 7. once it looks right, the tasks it was not shaped on
```

1. **Run.** `run.sh [subset] [harbor run flags]` takes a subset from [subsets](subsets) (default `smoke`) or `all` for the 89 tasks. Before it starts it checks that Docker runs and that a container downloads over HTTP and HTTPS, downloads the tasks into `~/harbor-tasks/terminal-bench-2.0` (`TASKS_DIR`) the first time, and rebuilds `eval/build` with [build.sh](build.sh) when the code changed since the last build. The build is static, for amd64 as Terminal-Bench's images are (`ARCHES` for others), and runs in Docker as SQLite needs cgo; it takes about ten minutes under emulation on Apple silicon. Flags after the subset go to `harbor run`, and `-i` runs the tasks it names instead of the subset. `MODEL` picks the model (default `deepseek/deepseek-flash`), `N` how many trials run at once (default 2).
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
- `probe`: eleven everyday tasks of ollupi's own, [below](#probe), for how much a task costs rather than whether it can be done.

Both subsets were picked by hand, each task needing 2 GB and none a virtual machine or a large model, so their scores compare versions of ollupi, not ollupi with the leaderboard.

## Probe

Terminal-Bench asks whether a hard task gets done. The probe asks what an easy one costs: the model responses, tool calls and tokens ollupi spends on the kind of request a person sends it every day, so that a change to the prompt or the tools can be told to make it cheaper without making it worse. Every task is one a capable agent finishes in a few calls; spending more is what to look at.

[probe/tasks.py](probe/tasks.py) holds the tasks and writes them as Harbor tasks into `probe/build`; `run.sh probe` does that first and builds ollupi for Docker's own architecture, as the probe's images are. Each image is the workspace [probe/fixture.py](probe/fixture.py) builds in `/app`, the same in every task: sales data, an application log, thirty meeting notes, a small Python tool with a failing test, and ollupi's own source at a fixed tag. The fixture is seeded, so tasks.py works out the answers from it, and [probe/check.py](probe/check.py) checks the agent's answers and files against them.

| Task | Asks for | A good run |
|---|---|---|
| probe-no-tools | a short explanation | no tool call |
| probe-code-locate | where a constant is defined in a large codebase | a search and a read |
| probe-code-explain | how a mechanism works across files | a few searches and ranged reads |
| probe-fix-test | fixing a failing test | read, edit, run the test once |
| probe-csv-aggregate | sums over 20,000 rows | one script, the file never read into context |
| probe-log-triage | the errors in a 30,000-line log and their cause | counts by command, a look before the first error |
| probe-notes-lookup | one fact among thirty notes | one search |
| probe-notes-compare | three named notes compared | three reads in one response |
| probe-notes-summary | a file summarizing all thirty notes | the notes read in a few calls, not thirty rounds |
| probe-followup | three messages, each building on the last | nothing found again that an earlier message found |
| probe-web-fact | a date from the web | one or two fetches |

A task of several messages separates them in `instruction.md` with a line `=== next message ===`; the eval build sends each as its own message in one conversation, once the turn before it ends.

`report.py` gives every task a second line, what its trials spent: model responses (`r`), tool calls (`t`) and input/output tokens, for Terminal-Bench's tasks too. `trace.py` ends with what is worth a look: a call made again, a file read again with no change in between, files read through the shell instead of read_file, grep and glob, `sleep`, outputs over 20,000 characters, failed calls, and runs of responses that each made a single read-only call where one response could have made them all.

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

[harbor/ollupi_agent.py](harbor/ollupi_agent.py) is the Harbor agent. It uploads the build into each task's container and runs it there in the task's folder, with the variables above, the task in `OLLUPI_INSTRUCTION` and the model, Harbor's `-m` without its provider prefix, in `OLLUPI_MODEL`.

The build, [main.go](main.go), runs one task through ollupi's agent and exits: one user, one conversation, a turn for each message of the task (most have one), no gateway. The container is the sandbox, so commands run with full access and nothing asks for approval; a question the model asks is answered that no one is there to answer. It is meant only for a task's container: on any other machine its commands would run on that machine unconfined. It prints the last message, keeps the turns' items in `-out`, rewritten as each completes, and exits 1 when a turn does not complete.

Each trial keeps `agent/ollupi.txt` (the log), `agent/ollupi-items.json` (the turn), `verifier/test-stdout.txt` and `result.json` under `eval/jobs/<job>/<task>__<id>/`; [trials.py](trials.py) reads them for the two scripts.
