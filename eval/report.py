#!/usr/bin/env python3
"""Compare ollupi's latest version with the earlier ones on the trials under eval/jobs; see eval/README.md.

    python3 eval/report.py [--all]
"""

import argparse
import re
from collections import defaultdict
from dataclasses import dataclass

from trials import read_all


@dataclass
class Result:
    """A task's tested trials in one version, from the latest job that tested it."""

    passed: int
    runs: int
    minutes: float
    timeout: bool
    version: str

    @property
    def rate(self):
        return self.passed / self.runs

    def cell(self, tagged=False):
        mark = "pass" if self.passed == self.runs else "FAIL" if self.passed == 0 else f"{self.passed}/{self.runs}"
        return f"{mark} {self.minutes:4.1f}m" + (" T/O" if self.timeout else "") + (f" @{short(self.version)}" if tagged else "")


def summarize(trials):
    latest = max(t.job for t in trials)
    run = [t for t in trials if t.job == latest]
    return Result(sum(t.passed for t in run), len(run), sum(t.minutes or 0 for t in run) / len(run),
                  any(t.timeout for t in run), run[0].version)


def short(version):
    """The commit of a version such as v0.12.1-7-gaeaf673-dirty+1a2b3c4d, with -dirty when it had changes."""
    m = re.search(r"-g([0-9a-f]+)(-dirty)?", version)
    return m.group(1) + (m.group(2) or "") if m else version


def main():
    p = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    p.add_argument("--all", action="store_true", help="a column for every version, instead of the latest against before")
    a = p.parse_args()

    # A version is a commit run with a model; versions are ordered by their first job.
    trials = defaultdict(list)
    for t in read_all():
        trials[t.version, t.model].append(t)
    if not trials:
        raise SystemExit("no trials under eval/jobs")
    versions = sorted(trials, key=lambda v: trials[v][0].job)
    tasks = sorted({t.task for ts in trials.values() for t in ts})

    results, untested = {}, {}
    for v in versions:
        for task in tasks:
            mine = [t for t in trials[v] if t.task == task]
            tested = [t for t in mine if t.tested]
            if tested:
                results[v, task] = summarize(tested)
            elif mine:
                untested[v, task] = mine[-1].path

    def column(v):
        cells = {task: results[v, task].cell() if (v, task) in results else
                 "not tested" if (v, task) in untested else "-" for task in tasks}
        return f"{v[0]} · {v[1]}", cells, {task: results.get((v, task)) for task in tasks}

    latest = versions[-1]
    if a.all or len(versions) == 1:
        columns = [column(v) for v in versions]
    else:
        # Before is each task's latest tested result in any earlier version, tagged with that version.
        before = {task: next((results[v, task] for v in reversed(versions[:-1]) if (v, task) in results), None)
                  for task in tasks}
        cells = {task: r.cell(tagged=True) if r else "-" for task, r in before.items()}
        columns = [("before", cells, before), column(latest)]
        untested = {k: path for k, path in untested.items() if k[0] == latest}

    compare = len(columns) == 2
    width = max(map(len, tasks)) + 2
    widths = [max(len(label), 16) + 2 for label, _, _ in columns]
    print("task".ljust(width) + "".join(label.rjust(w) for (label, _, _), w in zip(columns, widths))
          + ("   change" if compare else ""))
    for task in tasks:
        line = task.ljust(width) + "".join(cells[task].rjust(w) for (_, cells, _), w in zip(columns, widths))
        if compare and all(r[task] for _, _, r in columns):
            was, now = (r[task].rate for _, _, r in columns)
            line += "   fixed" if now > was else "   BROKE" if now < was else ""
        print(line)
    print()
    for label, _, r in columns:
        done = [x for x in r.values() if x]
        print(f"{label}: {sum(x.rate for x in done):g}/{len(done)} passed, {sum(x.minutes for x in done):.1f} agent minutes")
    if compare:
        both = [task for task in tasks if all(r[task] for _, _, r in columns)]
        if both:
            was, now = (sum(r[task].rate for task in both) for _, _, r in columns)
            print(f"on the {len(both)} tasks both tested: {was:g} passed before, {now:g} now")
    if untested:
        print("\nNot tested, because the verifier never ran the tests; read verifier/test-stdout.txt and run again:")
        for ((version, _), _), path in untested.items():
            print(f"  {version}  {path}")


if __name__ == "__main__":
    main()
