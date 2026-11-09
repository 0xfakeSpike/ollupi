# Eval

Measures what ollupi gets done with a real model, on public benchmarks, where [bench](../bench/README.md) measures scheduling and persistence with a simulated one. The score belongs to the model and the harness together: the same model scores differently in different harnesses, so a result means something only next to another harness run with the same model on the same dataset version.

`eval` is a command that runs one task through ollupi's agent and exits: one user, one conversation, one turn, no gateway. A benchmark runs it inside each task's container, which is the sandbox, so commands run with full access and nothing asks for approval; a question the model asks the user is answered with "decide for yourself". It prints the agent's last message, writes the turn's items as JSON with `-out`, and exits 1 when the turn does not complete.

## Running a task by hand

```bash
go build -tags sqlite_fts5 -o ollupi-eval ./eval
cd some/task/folder
OLLUPI_API_KEY=... OLLUPI_INSTRUCTION='Write primes.py that prints the first 20 primes and run it.' \
  /path/to/ollupi-eval -sandbox workspace-write -base-url https://api.deepseek.com -model deepseek-flash -out items.json
```

On macOS, `-sandbox workspace-write` confines commands to the folder; leave it at `full-access` only inside a container. `-api` is `openai` (also for compatible providers), `anthropic` or `gemini`; `-context` and `-output` give the model's limits.

## Harbor

[Harbor](https://harborframework.com) runs Terminal-Bench 2.0, SWE-bench Verified and other datasets in Docker, and [harbor/ollupi_agent.py](harbor/ollupi_agent.py) runs ollupi as one of its agents. It uploads the static Linux build into each task's container and runs the task there.

```bash
./eval/build.sh                    # eval/build/ollupi-eval-linux-{amd64,arm64}, built in Docker
uv tool install --python 3.12 harbor==0.25.0
export OLLUPI_API_KEY=... OLLUPI_BASE_URL=https://api.deepseek.com
PYTHONPATH=eval/harbor harbor run -d terminal-bench@2.0 -m deepseek/deepseek-v4-pro -a ollupi_agent:Ollupi -l 10 -n 2
```

`-m` is the model with its provider prefix, which the adapter drops. The key and the other settings (`OLLUPI_API`, `OLLUPI_BASE_URL`, `OLLUPI_CONTEXT`, `OLLUPI_OUTPUT`) come from the host's environment, so no key is on a command line. Each trial keeps `ollupi.txt` (the log) and `ollupi-items.json` (every item of the turn) under its `agent` folder in the job directory. Rebuild with `build.sh` after changing ollupi: the version the binary reports is the commit it was built from.

## Making a result count

- Pin everything: the dataset version (`terminal-bench@2.0`), the model, Harbor's version and ollupi's commit.
- Run each configuration more than once and report the mean and the spread; Terminal-Bench's leaderboard takes five runs (`--n-attempts 5`).
- Run another harness with the same model next to it, such as Harbor's own `-a terminus-2`, or Claude Code, Codex or pi.
- Read the failed trials' items and sort them by cause before changing the harness: a score alone does not say what to fix.
- Check that the containers reach the internet. Terminal-Bench's verifiers install curl and uv with `apt-get` before they test anything, so a container that cannot reach the Debian mirrors scores 0 whatever the agent did; `verifier/test-stdout.txt` shows it.
- Start with a few tasks and a cheap model to check the setup; a full run costs real money.
