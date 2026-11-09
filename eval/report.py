#!/usr/bin/env python3
"""Compare agents on the trials Harbor kept under a jobs folder (default eval/jobs).

    python3 eval/report.py [jobs folder]

A trial counts only when its verifier ran the tests: Terminal-Bench's verifiers write verifier/ctrf.json from
pytest, and a trial without it scored 0 because the tests never ran (the container could not install them, or the
environment did not start), not because of what the agent did. Those are listed to run again.

For each agent and model and each task, the latest job with a counted trial of that task gives its result: a rerun
of a task replaces the earlier one, and with --n-attempts the job's attempts give a pass rate. Agent time runs from
the agent's start to its end; T/O marks a trial the agent timed out in.
"""

import json
import sys
from datetime import datetime
from pathlib import Path


def when(s):
    return datetime.fromisoformat(s.replace("Z", "+00:00")) if s else None


def trials(jobs):
    for result in sorted(jobs.glob("*/*/*/result.json")):
        trial = result.parent
        r = json.loads(result.read_text())
        info = r.get("agent_info") or {}
        model = (info.get("model_info") or {}).get("name", "?")
        run = r.get("agent_execution") or {}
        start, end = when(run.get("started_at")), when(run.get("finished_at"))
        reward = trial / "verifier" / "reward.txt"
        yield {
            "group": f"{info.get('name', '?')} · {model}",
            "version": info.get("version") or "",
            "task": r.get("task_name") or trial.name.split("__")[0],
            "job": trial.parent.name,
            "counted": (trial / "verifier" / "ctrf.json").is_file() and reward.is_file(),
            "passed": reward.is_file() and float(reward.read_text().strip() or 0) >= 1,
            "minutes": (end - start).total_seconds() / 60 if start and end else None,
            "timeout": ((r.get("exception_info") or {}).get("exception_type")) == "AgentTimeoutError",
            "path": trial,
        }


def main():
    jobs = Path(sys.argv[1] if len(sys.argv) > 1 else Path(__file__).parent / "jobs")
    all_trials = list(trials(jobs))
    if not all_trials:
        sys.exit(f"no trials under {jobs}")
    groups = sorted({t["group"] for t in all_trials})
    tasks = sorted({t["task"] for t in all_trials})

    cells, totals, rerun = {}, {g: [0, 0, 0.0] for g in groups}, []
    for g in groups:
        for task in tasks:
            mine = [t for t in all_trials if t["group"] == g and t["task"] == task]
            counted = [t for t in mine if t["counted"]]
            if not counted:
                cells[g, task] = "not tested" if mine else "-"
                if mine:
                    rerun.append((g, task, mine[-1]["path"]))
                continue
            latest = max(t["job"] for t in counted)
            run = [t for t in counted if t["job"] == latest]
            passed = sum(t["passed"] for t in run)
            minutes = sum(t["minutes"] or 0 for t in run) / len(run)
            mark = ("pass" if passed == len(run) else "FAIL" if passed == 0 else f"{passed}/{len(run)}")
            cells[g, task] = f"{mark} {minutes:4.1f}m" + (" T/O" if any(t["timeout"] for t in run) else "")
            totals[g][0] += passed / len(run)
            totals[g][1] += 1
            totals[g][2] += minutes

    width = max(len(t) for t in tasks) + 2
    cols = [max(len(g), 14) + 2 for g in groups]
    print("task".ljust(width) + "".join(g.rjust(c) for g, c in zip(groups, cols)))
    for task in tasks:
        print(task.ljust(width) + "".join(cells[g, task].rjust(c) for g, c in zip(groups, cols)))
    print()
    for g in groups:
        passed, counted, minutes = totals[g]
        versions = sorted({t["version"] for t in all_trials if t["group"] == g and t["counted"]})
        print(f"{g}: {passed:g}/{counted} passed, {minutes:.1f} agent minutes; versions {', '.join(versions)}")
    if rerun:
        print("\nNot tested, because the verifier never ran the tests; read verifier/test-stdout.txt and run again:")
        for g, task, path in rerun:
            print(f"  {g}  {task}  ({path})")


if __name__ == "__main__":
    main()
