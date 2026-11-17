#!/usr/bin/env python3
"""Show what ollupi did in one trial, and what the tests said of it; see eval/README.md.

    python3 eval/trace.py <trial folder> [--full]
"""

import argparse
import json
import re
from collections import defaultdict
from pathlib import Path

from trials import Trial, when


def clipper(full):
    """Shortens a text to one line of n characters, unless every text is to be printed whole."""

    def clip(s, n=150):
        if full:
            return s
        line = " ".join(str(s).split())
        return line if len(line) <= n else line[:n] + "…"

    return clip


def show_tests(stdout, tested, clip):
    """The verifier's summary line and failing assertions, or the end of its output when the tests never ran."""
    if not stdout.is_file():
        return
    lines = stdout.read_text(errors="replace").splitlines()
    summary = [l for l in lines if re.match(r"^=+ .*(passed|failed|error).* =+$", l)]
    failures = [l for l in lines if l.startswith(("FAILED ", "ERROR ")) or re.match(r"^E\s+\S", l)]
    if summary:
        print("tests: " + summary[-1].strip("= "))
    for l in failures[:12]:
        print("  " + clip(l, 200))
    if not tested:
        print("  the tests never ran; the end of verifier/test-stdout.txt:")
        for l in lines[-5:]:
            print("  " + clip(l, 200))


def answered(items):
    """When each model response was done: at its first tool call, or else at the end of its text."""
    calls, texts = defaultdict(list), defaultdict(list)
    for it in items:
        rid = it.get("responseId")
        if rid and it["type"] == "toolCall" and it.get("startedAt"):
            calls[rid].append(when(it["startedAt"]))
        elif rid and it["type"] != "toolCall" and it.get("completedAt"):
            texts[rid].append(when(it["completedAt"]))
    return {rid: min(calls[rid]) if calls[rid] else max(texts[rid]) for rid in calls.keys() | texts.keys()}


def output_size(it):
    output = (it.get("result") or {}).get("output")
    if isinstance(output, dict) and "exit_code" in output:
        return f" exit {output['exit_code']}, {len(output.get('stdout') or '') + len(output.get('stderr') or '')} chars"
    return "" if output is None else f" {len(json.dumps(output))} chars"


def show_turn(turn, items, clip):
    """The turn as a timeline of model responses and tool calls, then where its time went."""
    t0 = when(turn.get("startedAt") or turn.get("createdAt"))

    def at(s):
        if not s:
            return "  ?  "
        secs = (when(s) - t0).total_seconds()
        return f"{int(secs // 60):02d}:{int(secs % 60):02d}"

    print(f"\nturn {turn.get('status')}, {len(items)} items")
    print("task: " + clip(turn.get("input") or items[0].get("text", ""), 300))
    print()

    done = answered(items)
    model_secs, responses, asked, response = 0.0, 0, t0, None
    tools = defaultdict(lambda: [0, 0.0])
    compactions, peak, budget = 0, 0, 0
    for it in items:
        start, end, kind = when(it.get("startedAt")), when(it.get("completedAt")), it["type"]
        if it.get("responseId") and it["responseId"] != response:
            response = it["responseId"]
            responses += 1
            finished = done.get(response)
            wait = max((finished - asked).total_seconds(), 0) if finished and asked else 0
            model_secs += wait
            print(f"{at(it.get('startedAt'))}  model   {wait:5.1f}s")
            asked = finished or asked
        stats = it.get("contextStats") or {}
        if stats:
            peak = max(peak, stats.get("calibratedTokens") or stats.get("estimatedTokens") or 0)
            budget = stats.get("budgetTokens") or budget
        if kind in ("reasoning", "agentMessage"):
            print(f"         {'thinks' if kind == 'reasoning' else 'says':8}{clip(it.get('text', ''))}")
        elif kind == "toolCall":
            secs = (end - start).total_seconds() if start and end else 0
            tools[it["tool"]][0] += 1
            tools[it["tool"]][1] += secs
            status = "" if it.get("status") == "completed" else f" [{it.get('status')}]"
            print(f"{at(it.get('startedAt'))}  tool    {secs:5.1f}s  {it['tool']}{status}{output_size(it)}  "
                  f"{clip(it.get('arguments', ''), 120)}")
            if it.get("error"):
                print(f"         error   {clip(it['error'])}")
        elif kind == "contextCompaction":
            compactions += 1
            print(f"{at(it.get('startedAt'))}  context compacted")
        elif kind == "error":
            print(f"{at(it.get('startedAt'))}  error   {clip(it.get('error') or it.get('text', ''))}")
        if end and kind in ("toolCall", "userMessage", "contextCompaction"):
            asked = max(asked, end) if asked else end

    last = items[-1] if items else {}
    total = (when(last.get("completedAt") or last.get("startedAt")) - t0).total_seconds() if items else 0
    calls = sum(n for n, _ in tools.values())
    print(f"\n{total / 60:.1f} minutes: {model_secs / 60:.1f} waiting on the model ({responses} responses), "
          f"{sum(s for _, s in tools.values()) / 60:.1f} in tools ({calls} calls)")
    for tool, (n, secs) in sorted(tools.items(), key=lambda kv: -kv[1][0]):
        print(f"  {tool:16} {n:3} calls  {secs:6.1f}s")
    if budget:
        print(f"context: peak {peak} of {budget} tokens" + (f", compacted {compactions} times" if compactions else ""))


def main():
    p = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    p.add_argument("trial", type=Path, help="eval/jobs/<job>/<task>__<id>")
    p.add_argument("--full", action="store_true", help="print texts and arguments whole")
    a = p.parse_args()
    clip = clipper(a.full)

    trial = Trial.read(a.trial)
    print(f"{trial.task} · {trial.version} · {trial.model}")
    outcome = ("passed" if trial.passed else "failed") if trial.tested else "not tested"
    print(f"outcome: {outcome}" + (f", {trial.exception}" if trial.exception else ""))
    show_tests(a.trial / "verifier" / "test-stdout.txt", trial.tested, clip)

    items = a.trial / "agent" / "ollupi-items.json"
    if not items.is_file():
        print(f"\nno {items.name}: the agent did not start")
        log = a.trial / "agent" / "ollupi.txt"
        if log.is_file():
            print("the end of agent/ollupi.txt:")
            for line in log.read_text(errors="replace").splitlines()[-15:]:
                print("  " + clip(line, 200))
        return
    data = json.loads(items.read_text())
    show_turn(data["turn"], data["items"], clip)


if __name__ == "__main__":
    main()
