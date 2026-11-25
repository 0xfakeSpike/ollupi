#!/usr/bin/env python3
"""Writes the probe tasks as Harbor tasks, each with its answers worked out from the fixture; see eval/README.md.

    python3 eval/probe/tasks.py <out dir>
"""

import argparse
import json
import shutil
import tempfile
from pathlib import Path

import fixture

HERE = Path(__file__).resolve().parent

NEXT = "\n=== next message ===\n"

DOCKERFILE = """FROM python:3.12-slim-bookworm
WORKDIR /app
COPY fixture.py /tmp/fixture.py
RUN python3 /tmp/fixture.py /app && rm /tmp/fixture.py
"""

TEST_SH = """#!/bin/sh
python3 /tests/check.py
"""

TASK_TOML = """version = "1.0"

[metadata]
category = "probe"
tags = ["probe", "%s"]

[verifier]
timeout_sec = 120.0

[agent]
timeout_sec = 900.0

[environment]
build_timeout_sec = 600.0
cpus = 1
memory_mb = 2048
"""


def any_of(*needles, turn=-1):
    return {"kind": "any", "of": list(needles), "turn": turn}


def day(iso, turn=-1):
    y, m, d = iso.split("-")
    return any_of(iso, "%d月%d日" % (int(m), int(d)), "%s-%s" % (m, d), turn=turn)


def number(value, turn=-1, tolerance=0.002):
    return {"kind": "number", "value": value, "turn": turn, "tolerance": tolerance}


def tasks(a):
    by_region = a["sales_2025_by_region"]
    errors = a["log_errors"]
    topics = a["note_topics"]
    compared = [a["notes_tentative"], a["notes_delay"], a["notes_outing"]]
    return [
        {"name": "probe-no-tools", "tag": "no-tools",
         "messages": ["Go 语言里 slice 和 array 有什么区别？三句话说清楚。"],
         "checks": [any_of("长度", "length")]},
        {"name": "probe-code-locate", "tag": "code",
         "messages": ["code/ 是一个 Go 项目。一个 turn 里工具连续失败多少次，agent 就会停下？这个数定义在哪个文件里？"],
         "checks": [any_of("10"), any_of("agent/agent.go", "agent.go")]},
        {"name": "probe-code-explain", "tag": "code",
         "messages": ["code/ 是一个 Go 项目。工具结果太长时，它是怎么截断后给模型看的？模型之后怎么拿到被截掉的部分？简要说明，并指出关键文件。"],
         "checks": [any_of("read_tool_output"), any_of("observation.go")]},
        {"name": "probe-fix-test", "tag": "edit",
         "messages": ["tools/ 里 invoice 的测试有一个没通过，帮我修好，不要改测试。"],
         "checks": [{"kind": "command", "argv": ["python3", "-m", "unittest", "-q", "test_invoice"], "cwd": "/app/tools"},
                    {"kind": "sha256", "path": "/app/tools/test_invoice.py", "value": a["invoice_test_sha256"]}]},
        {"name": "probe-csv-aggregate", "tag": "data",
         "messages": ["data/sales.csv 里，2025 年每个地区的销售额（qty × unit_price）分别是多少？从高到低列出来。"],
         "checks": [number(v) for v in by_region.values()] + [{"kind": "order", "of": list(by_region), "turn": -1}]},
        {"name": "probe-log-triage", "tag": "data",
         "messages": ["logs/app.log 里出现了哪些错误，各多少次？最主要的那类错误是什么原因引起的？"],
         "checks": [number(n, tolerance=0) for n in errors.values()] + [any_of("pool_size", "pool size", "连接池大小")]},
        {"name": "probe-notes-lookup", "tag": "notes",
         "messages": ["notes/ 里是哪次会议决定推迟年底大版本发布的？推迟到哪天？谁提的，为什么？"],
         "checks": [day(a["notes_delay"]), any_of("12月8日", "12-08", "12/8"), any_of("王磊"), any_of("回归测试")]},
        {"name": "probe-notes-compare", "tag": "notes",
         "messages": ["对比一下 %s 这三次会议的结论。" % "、".join("notes/%s.md" % n for n in compared)],
         "checks": [any_of(topics[n]) for n in compared] + [any_of("12月8日", "12-08", "12/8"), any_of("团建")]},
        {"name": "probe-notes-summary", "tag": "notes",
         "messages": ["根据 notes/ 里的全部会议记录，写一份 notes/summary.md：每次会议一行，格式是 `- 日期：议题 — 结论`，按日期从早到晚排。"],
         "checks": [{"kind": "lines", "path": "/app/notes/summary.md", "match": r"^- (\d{4}-\d{2}-\d{2})", "keys": a["notes"],
                     "contains": sorted(set(topics.values()))}]},
        {"name": "probe-followup", "tag": "multi-turn",
         "messages": ["data/sales.csv 有哪些列？一共多少行数据？",
                      "West 地区 2024 年 Widget 一共卖了多少件？",
                      "这个数占 West 地区 2024 年全部件数的百分之几？"],
         "checks": [number(a["sales_rows"], turn=0, tolerance=0), number(a["west_2024_widget_qty"], turn=1, tolerance=0),
                    number(a["west_2024_widget_share"], turn=2, tolerance=0.005)]},
        {"name": "probe-web-fact", "tag": "web",
         "messages": ["Go 1.23 是哪天正式发布的？"],
         "checks": [any_of("2024-08-13", "2024年8月13日", "August 13, 2024", "13 August 2024")]},
    ]


def write(out, task):
    d = out / task["name"]
    (d / "environment").mkdir(parents=True)
    (d / "tests").mkdir()
    (d / "instruction.md").write_text(NEXT.join(task["messages"]) + "\n")
    (d / "task.toml").write_text(TASK_TOML % task["tag"])
    (d / "environment" / "Dockerfile").write_text(DOCKERFILE)
    shutil.copy(HERE / "fixture.py", d / "environment" / "fixture.py")
    (d / "tests" / "test.sh").write_text(TEST_SH)
    (d / "tests" / "test.sh").chmod(0o755)
    shutil.copy(HERE / "check.py", d / "tests" / "check.py")
    (d / "tests" / "expect.json").write_text(json.dumps({"name": task["name"], "checks": task["checks"]}, ensure_ascii=False, indent=2))


def main():
    p = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    p.add_argument("out", type=Path)
    out = p.parse_args().out
    with tempfile.TemporaryDirectory() as tmp:
        answers = fixture.build(tmp, with_code=False)
    shutil.rmtree(out, ignore_errors=True)
    out.mkdir(parents=True)
    for t in tasks(answers):
        write(out, t)


if __name__ == "__main__":
    main()
