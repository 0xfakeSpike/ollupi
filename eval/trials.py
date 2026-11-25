"""The trials Harbor keeps under eval/jobs: which version ran which task, and what the verifier said."""

from __future__ import annotations

import json
import re
from dataclasses import dataclass
from datetime import datetime
from pathlib import Path

JOBS = Path(__file__).resolve().parent / "jobs"


def when(s):
    """A time as Harbor or ollupi wrote it, some with more fractional digits than Python reads."""
    return datetime.fromisoformat(re.sub(r"(\.\d{6})\d+", r"\1", s).replace("Z", "+00:00")) if s else None


@dataclass
class Trial:
    path: Path
    task: str
    job: str
    agent: str
    version: str
    model: str
    minutes: float | None
    exception: str | None
    tested: bool
    passed: bool
    cost: "Cost | None"

    @property
    def timeout(self):
        return self.exception == "AgentTimeoutError"

    @classmethod
    def read(cls, path):
        result = path / "result.json"
        r = json.loads(result.read_text()) if result.is_file() else {}
        info = r.get("agent_info") or {}
        run = r.get("agent_execution") or {}
        start, end = when(run.get("started_at")), when(run.get("finished_at"))
        reward = path / "verifier" / "reward.txt"
        return cls(
            path=path,
            task=r.get("task_name") or path.name.split("__")[0],
            job=path.parent.name,
            agent=info.get("name", "?"),
            version=info.get("version") or "?",
            model=(info.get("model_info") or {}).get("name", "?"),
            minutes=(end - start).total_seconds() / 60 if start and end else None,
            exception=(r.get("exception_info") or {}).get("exception_type"),
            tested=(path / "verifier" / "ctrf.json").is_file() and reward.is_file(),
            passed=reward.is_file() and float(reward.read_text().strip() or 0) >= 1,
            cost=Cost.read(path / "agent" / "ollupi-items.json"),
        )


@dataclass
class Cost:
    """What a trial's turns spent: model responses, tool calls and tokens."""

    responses: int
    calls: int
    failed: int
    input: int
    cached: int
    output: int

    @classmethod
    def read(cls, path):
        if not path.is_file():
            return None
        data = json.loads(path.read_text())
        items, usage = data["items"], data["turn"].get("usage") or {}
        calls = [it for it in items if it["type"] == "toolCall"]
        return cls(
            responses=len({it["responseId"] for it in items if it.get("responseId")}),
            calls=len(calls),
            failed=sum(it.get("status") != "completed" for it in calls),
            input=usage.get("inputTokens", 0),
            cached=usage.get("cachedInputTokens", 0),
            output=usage.get("outputTokens", 0),
        )


def read_all(jobs=JOBS):
    """ollupi's trials, in the order their jobs ran."""
    for result in sorted(jobs.glob("*/*/result.json")):
        trial = Trial.read(result.parent)
        if trial.agent == "ollupi":
            yield trial
