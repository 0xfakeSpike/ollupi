#!/usr/bin/env python3
"""Show what ollupi did in one Harbor trial, and what the tests said about it.

    python3 eval/trace.py <trial folder> [--full]

The trial folder is eval/jobs/<agent>/<job>/<task>__<id>. It prints the trial's outcome, the failing tests and
their assertions, then the turn as a timeline: each model response with the time it took and what the model said,
each tool call with its arguments, time, exit code and size of output, and context compactions. A summary ends it:
where the time went, the calls per tool, and how full the context got. Text is clipped to a line unless --full.

It reads agent/ollupi-items.json, which eval rewrites as each item completes, so a run the harness stopped at its
time limit shows up to that point.
"""

import argparse
import json
import re
from collections import defaultdict
from datetime import datetime
from pathlib import Path


def when(s):
    return datetime.fromisoformat(re.sub(r"(\.\d{6})\d+", r"\1", s).replace("Z", "+00:00")) if s else None


def main():
    p = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    p.add_argument("trial", type=Path)
    p.add_argument("--full", action="store_true", help="print texts and arguments whole")
    a = p.parse_args()
    trial = a.trial
    clip = (lambda s, n=150: s) if a.full else (lambda s, n=150: (lambda t: t if len(t) <= n else t[:n] + "…")(" ".join(str(s).split())))

    result = json.loads((trial / "result.json").read_text()) if (trial / "result.json").is_file() else {}
    info = result.get("agent_info") or {}
    reward = trial / "verifier" / "reward.txt"
    exception = (result.get("exception_info") or {}).get("exception_type")
    print(f"{result.get('task_name', trial.name)} · {info.get('name', '?')}@{info.get('version', '?')} · "
          f"{(info.get('model_info') or {}).get('name', '?')}")
    tested = (trial / "verifier" / "ctrf.json").is_file()
    outcome = ("passed" if reward.is_file() and float(reward.read_text().strip() or 0) >= 1 else "failed") if tested else "not tested"
    print(f"outcome: {outcome}" + (f", {exception}" if exception else ""))
    stdout = trial / "verifier" / "test-stdout.txt"
    if stdout.is_file():
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

    items_file = trial / "agent" / "ollupi-items.json"
    if not items_file.is_file():
        print(f"\nno {items_file.name}: the agent did not start, is not ollupi, or ran a build of eval from before it "
              "kept the file during the run")
        log = trial / "agent" / "ollupi.txt"
        if log.is_file():
            print("the end of agent/ollupi.txt:")
            for line in log.read_text(errors="replace").splitlines()[-15:]:
                print("  " + clip(line, 200))
        return
    data = json.loads(items_file.read_text())
    turn, items = data["turn"], data["items"]
    t0 = when(turn.get("startedAt") or turn.get("createdAt"))
    at = lambda s: (lambda d: f"{int(d // 60):02d}:{int(d % 60):02d}")((when(s) - t0).total_seconds()) if s else "  ?  "
    print(f"\nturn {turn.get('status')}, {len(items)} items")
    print("task: " + clip(turn.get("input") or items[0].get("text", ""), 300))
    print()

    # A model response is a run of items with one responseId: its text, then the calls it made. The model's time runs
    # from when it was asked (the end of what came before) to its first call, or to the end of its text.
    answered = {}
    for i, it in enumerate(items):
        rid = it.get("responseId")
        if not rid or rid in answered:
            continue
        group = [x for x in items[i:] if x.get("responseId") == rid]
        calls = [when(x.get("startedAt")) for x in group if x["type"] == "toolCall" and x.get("startedAt")]
        texts = [when(x.get("completedAt")) for x in group if x["type"] != "toolCall" and x.get("completedAt")]
        answered[rid] = min(calls) if calls else max(texts, default=None)

    model_secs, tool_secs, per_tool, responses, compactions, peak, budget = 0.0, 0.0, defaultdict(lambda: [0, 0.0]), 0, 0, 0, 0
    asked, seen_response = t0, None
    for it in items:
        start, end = when(it.get("startedAt")), when(it.get("completedAt"))
        kind = it["type"]
        if it.get("responseId") and it["responseId"] != seen_response:
            seen_response = it["responseId"]
            responses += 1
            done = answered.get(seen_response)
            wait = max((done - asked).total_seconds(), 0) if done and asked else 0
            model_secs += wait
            print(f"{at(it.get('startedAt'))}  model   {wait:5.1f}s")
            asked = done or asked
        stats = it.get("contextStats") or {}
        if stats:
            peak, budget = max(peak, stats.get("calibratedTokens") or stats.get("estimatedTokens") or 0), stats.get("budgetTokens") or budget
        if kind in ("reasoning", "agentMessage"):
            label = "thinks " if kind == "reasoning" else "says  "
            print(f"         {label} {clip(it.get('text', ''))}")
        elif kind == "toolCall":
            secs = (end - start).total_seconds() if start and end else 0
            tool_secs += secs
            per_tool[it["tool"]][0] += 1
            per_tool[it["tool"]][1] += secs
            output = (it.get("result") or {}).get("output")
            detail = ""
            if isinstance(output, dict) and "exit_code" in output:
                detail = f" exit {output['exit_code']}, {len(output.get('stdout') or '') + len(output.get('stderr') or '')} chars"
            elif output is not None:
                detail = f" {len(json.dumps(output))} chars"
            status = "" if it.get("status") == "completed" else f" [{it.get('status')}]"
            print(f"{at(it.get('startedAt'))}  tool    {secs:5.1f}s  {it['tool']}{status}{detail}  {clip(it.get('arguments', ''), 120)}")
            if it.get("error"):
                print(f"         error   {clip(it['error'])}")
        elif kind == "contextCompaction":
            compactions += 1
            print(f"{at(it.get('startedAt'))}  context compacted")
        elif kind == "error":
            print(f"{at(it.get('startedAt'))}  error   {clip(it.get('error') or it.get('text', ''))}")
        if end and kind in ("toolCall", "userMessage", "contextCompaction"):
            asked = max(asked, end) if asked else end

    total = ((when(items[-1].get("completedAt") or items[-1].get("startedAt")) - t0).total_seconds()) if items else 0
    print(f"\n{total / 60:.1f} minutes: {model_secs / 60:.1f} waiting on the model ({responses} responses), "
          f"{tool_secs / 60:.1f} in tools ({sum(n for n, _ in per_tool.values())} calls)")
    for tool, (n, secs) in sorted(per_tool.items(), key=lambda kv: -kv[1][0]):
        print(f"  {tool:16} {n:3} calls  {secs:6.1f}s")
    if budget:
        print(f"context: peak {peak} of {budget} tokens" + (f", compacted {compactions} times" if compactions else ""))


if __name__ == "__main__":
    main()
