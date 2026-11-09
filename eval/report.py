#!/usr/bin/env python3
"""Compare the versions of an agent on the trials Harbor kept under eval/jobs.

    python3 eval/report.py [--last N | --all] [--agent NAME] [--jobs DIR]

By default it puts an agent's latest version, the commit eval was built from (see build.sh), next to what came
before it: for each task, the latest result of any earlier version, tagged with the version it came from, so that
running only the tasks a change is for still compares each of them. A last column says which tasks the change fixed
or broke. With --all each version gets a column of its own, oldest first.

A trial counts only when its verifier ran the tests: Terminal-Bench's verifiers write verifier/ctrf.json from
pytest, and a trial without it scored 0 because the tests never ran (the container could not install them, or the
environment did not start), not because of what the agent did. Those are listed to run again.

For each column and task, the latest job with a counted trial gives the result: a rerun of a task replaces the
earlier one, and with --n-attempts the job's attempts give a pass rate such as 3/5. Minutes are the agent's, from
its start to its end; T/O marks a trial the agent ran out of time in.
"""

import argparse
import json
import re
from datetime import datetime
from pathlib import Path


def when(s):
    return datetime.fromisoformat(s.replace("Z", "+00:00")) if s else None


def trials(jobs):
    for result in sorted(jobs.glob("*/*/*/result.json")):
        trial = result.parent
        r = json.loads(result.read_text())
        info = r.get("agent_info") or {}
        run = r.get("agent_execution") or {}
        start, end = when(run.get("started_at")), when(run.get("finished_at"))
        reward = trial / "verifier" / "reward.txt"
        yield {
            "agent": info.get("name", "?"),
            "column": (info.get("name", "?"), (info.get("model_info") or {}).get("name", "?"), info.get("version") or "?"),
            "task": r.get("task_name") or trial.name.split("__")[0],
            "job": trial.parent.name,
            "started": when(r.get("started_at")),
            "counted": (trial / "verifier" / "ctrf.json").is_file() and reward.is_file(),
            "passed": reward.is_file() and float(reward.read_text().strip() or 0) >= 1,
            "minutes": (end - start).total_seconds() / 60 if start and end else None,
            "timeout": ((r.get("exception_info") or {}).get("exception_type")) == "AgentTimeoutError",
            "path": trial,
        }


def result(runs):
    """The counted trials of a task in one column: (pass rate, mean minutes, any timeout), from its latest job."""
    latest = max(t["job"] for t in runs)
    run = [t for t in runs if t["job"] == latest]
    return (sum(t["passed"] for t in run) / len(run), sum(t["minutes"] or 0 for t in run) / len(run),
            any(t["timeout"] for t in run), len(run))


def cell(r):
    if r is None:
        return "-"
    if r == "untested":
        return "not tested"
    rate, minutes, timeout, n = r[:4]
    mark = "pass" if rate == 1 else "FAIL" if rate == 0 else f"{round(rate * n)}/{n}"
    return f"{mark} {minutes:4.1f}m" + (" T/O" if timeout else "") + (f" @{short(r[4])}" if len(r) > 4 else "")


def short(version):
    """The commit of a version such as v0.12.1-7-gaeaf673-dirty+1a2b3c4d, with -dirty when it had changes."""
    m = re.search(r"-g([0-9a-f]+)(-dirty)?", version)
    return (m.group(1) + ("-dirty" if m.group(2) else "")) if m else version


def main():
    p = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    p.add_argument("--jobs", type=Path, default=Path(__file__).parent / "jobs")
    p.add_argument("--agent", help="only this agent's columns")
    p.add_argument("--all", action="store_true", help="a column for every version, instead of the latest against before")
    a = p.parse_args()

    found = [t for t in trials(a.jobs) if not a.agent or t["agent"] == a.agent]
    if not found:
        raise SystemExit(f"no trials under {a.jobs}")
    first = {}
    for t in found:
        if t["started"] and (t["column"] not in first or t["started"] < first[t["column"]]):
            first[t["column"]] = t["started"]
    order = sorted({t["column"] for t in found}, key=lambda c: first.get(c) or datetime.min)
    tasks = sorted({t["task"] for t in found})

    results, rerun = {}, []
    for c in order:
        for task in tasks:
            mine = [t for t in found if t["column"] == c and t["task"] == task]
            counted = [t for t in mine if t["counted"]]
            if counted:
                results[c, task] = result(counted)
            elif mine:
                results[c, task] = "untested"
                rerun.append((c, task, max(mine, key=lambda t: t["job"])["path"]))

    if a.all or len(order) == 1:
        columns = order
        labels = [f"{agent}@{version} · {model}" for agent, model, version in columns]
    else:
        # Before is a column of its own: each task's latest tested result among the earlier versions.
        latest, earlier = order[-1], order[:-1]
        before = ("before",)
        for task in tasks:
            tested = [c for c in earlier if isinstance(results.get((c, task)), tuple)]
            if tested:
                results[before, task] = results[tested[-1], task] + (tested[-1][2],)
        columns = [before, latest]
        labels = ["before", f"{latest[0]}@{latest[2]} · {latest[1]}"]
        rerun = [r for r in rerun if r[0] == latest]
    compare = len(columns) == 2
    width = max(len(t) for t in tasks) + 2
    cols = [max(len(label), 16) + 2 for label in labels]
    print("task".ljust(width) + "".join(label.rjust(w) for label, w in zip(labels, cols)) + ("   change" if compare else ""))
    for task in tasks:
        row = [results.get((c, task)) for c in columns]
        line = task.ljust(width) + "".join(cell(r).rjust(w) for r, w in zip(row, cols))
        if compare and all(isinstance(r, tuple) for r in row):
            before, after = row[0][0], row[1][0]
            line += "   fixed" if after > before else "   BROKE" if after < before else ""
        print(line)
    print()
    for c, label in zip(columns, labels):
        done = [r for (col, _), r in results.items() if col == c and isinstance(r, tuple)]
        print(f"{label}: {sum(r[0] for r in done):g}/{len(done)} passed, {sum(r[1] for r in done):.1f} agent minutes")
    if compare and columns[0] == ("before",):
        both = [t for t in tasks if isinstance(results.get((columns[0], t)), tuple) and isinstance(results.get((columns[1], t)), tuple)]
        if both:
            b = sum(results[columns[0], t][0] for t in both)
            n = sum(results[columns[1], t][0] for t in both)
            print(f"on the {len(both)} tasks both tested: {b:g} passed before, {n:g} now")
    if rerun:
        print("\nNot tested, because the verifier never ran the tests; read verifier/test-stdout.txt and run again:")
        for (agent, model, version), task, path in rerun:
            print(f"  {agent}@{version}  {task}  ({path})")


if __name__ == "__main__":
    main()
