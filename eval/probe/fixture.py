#!/usr/bin/env python3
"""Builds the workspace the probe tasks run in: data, a log, meeting notes, a small Python tool and ollupi's source.

    python3 fixture.py <root> [--no-code]

Seeded, so every build is the same and tasks.py can work out the answers from it.
"""

import argparse
import csv
import hashlib
import io
import os
import random
import shutil
import tarfile
import urllib.request
from collections import Counter, defaultdict
from datetime import date, datetime, timedelta

CODE_TAG = "v0.35.1"
CODE_URL = "https://github.com/0xfakeSpike/ollupi/archive/refs/tags/%s.tar.gz" % CODE_TAG


def sales(root, answers):
    rnd = random.Random(7)
    regions = ["North", "South", "East", "West", "Central"]
    products = {"Widget": 12.5, "Gadget": 48.0, "Gizmo": 7.25, "Doohickey": 99.0}
    channels = ["online", "store", "partner"]
    start = date(2024, 1, 1)
    by_region_2025 = defaultdict(float)
    west_2024_widget = 0
    west_2024_all = 0
    rows = []
    for _ in range(20000):
        d = start + timedelta(days=rnd.randrange(731))
        region = rnd.choices(regions, weights=[3, 2, 4, 2, 1])[0]
        product = rnd.choice(list(products))
        qty = rnd.randint(1, 40)
        price = round(products[product] * rnd.uniform(0.9, 1.1), 2)
        rows.append([d.isoformat(), region, product, qty, f"{price:.2f}", rnd.choice(channels)])
        if d.year == 2025:
            by_region_2025[region] += qty * price
        if d.year == 2024 and region == "West":
            west_2024_all += qty
            if product == "Widget":
                west_2024_widget += qty
    rows.sort(key=lambda r: r[0])
    os.makedirs(os.path.join(root, "data"))
    with open(os.path.join(root, "data", "sales.csv"), "w", newline="") as f:
        w = csv.writer(f)
        w.writerow(["date", "region", "product", "qty", "unit_price", "channel"])
        w.writerows(rows)
    answers["sales_rows"] = len(rows)
    answers["sales_2025_by_region"] = {k: round(v, 2) for k, v in sorted(by_region_2025.items(), key=lambda kv: -kv[1])}
    answers["west_2024_widget_qty"] = west_2024_widget
    answers["west_2024_widget_share"] = round(100 * west_2024_widget / west_2024_all, 2)


def log(root, answers):
    rnd = random.Random(11)
    t = datetime(2025, 11, 3, 0, 0, 0)
    reload_at = 18000
    lines = []
    errors = Counter()
    infos = [
        "INFO  http: GET /api/orders 200 {ms}ms",
        "INFO  http: POST /api/checkout 200 {ms}ms",
        "INFO  http: GET /api/products 200 {ms}ms",
        "DEBUG cache: hit ratio 0.{ms}",
        "INFO  worker: job sync-inventory done in {ms}ms",
        "WARN  http: slow request GET /api/report {ms}ms",
    ]
    for i in range(30000):
        t += timedelta(seconds=rnd.randint(1, 4))
        ts = t.strftime("%Y-%m-%dT%H:%M:%S")
        if i == reload_at:
            lines.append(f"{ts} INFO  config: reloaded from /etc/shop/app.yaml (db.pool_size=5, was 50)")
            continue
        if i > reload_at and rnd.random() < 0.035:
            msg = "ERROR db: connection pool exhausted (size=5, waiting=%d)" % rnd.randint(20, 90)
            errors["db: connection pool exhausted"] += 1
        elif rnd.random() < 0.0015:
            msg = "ERROR payment: gateway timeout after 30s (provider=stripe)"
            errors["payment: gateway timeout"] += 1
        elif i in (4200, 22050, 27111):
            msg = "ERROR http: panic in handler /api/export: runtime error: invalid memory address or nil pointer dereference"
            errors["http: nil pointer in /api/export"] += 1
        else:
            msg = rnd.choice(infos).format(ms=rnd.randint(3, 900))
        lines.append(f"{ts} {msg}")
    os.makedirs(os.path.join(root, "logs"))
    with open(os.path.join(root, "logs", "app.log"), "w") as f:
        f.write("\n".join(lines) + "\n")
    answers["log_errors"] = dict(errors)
    answers["log_root_cause"] = "config reload at line %d set db.pool_size from 50 to 5; pool-exhausted errors start after it" % (reload_at + 1)


PEOPLE = ["王磊", "李娜", "张伟", "刘洋", "陈静", "赵敏"]
TOPICS = [
    ("首页改版", "首页改版按 B 方案做，两周内出高保真稿。"),
    ("客服工单积压", "增加一名夜班客服，工单 SLA 改为 4 小时。"),
    ("搜索排序", "搜索排序加入库存权重，下周灰度 10%。"),
    ("会员积分", "积分有效期从 1 年延长到 2 年。"),
    ("数据看板", "看板只保留 8 个核心指标，其余移到明细页。"),
    ("供应商对账", "对账改为每周一次，财务出模板。"),
    ("移动端崩溃", "崩溃率目标降到 0.3% 以下，先修 Top 5 崩溃。"),
    ("物流时效", "华东仓切换到新承运商，月底评估时效。"),
    ("招聘", "后端再招两人，优先有支付经验的。"),
    ("安全审计", "外部安全审计安排在 9 月，先自查权限配置。"),
]


TENTATIVE, DELAY, OUTING = 7, 20, 26


def notes(root, answers):
    rnd = random.Random(3)
    d = os.path.join(root, "notes")
    os.makedirs(d)
    day = date(2025, 1, 6)
    written = []
    for i in range(30):
        name = day.isoformat()
        topic, decision = TOPICS[i % len(TOPICS)]
        attendees = rnd.sample(PEOPLE, 4)
        if i == DELAY and "王磊" not in attendees:
            attendees[0] = "王磊"
        body = [f"# 周会 {name}", "", "参会：" + "、".join(attendees), "", f"## 议题：{topic}", ""]
        body += [f"- {rnd.choice(attendees)} 汇报了{topic}的进展。", f"- 讨论了{topic}的风险和排期。", ""]
        conclusion = decision
        if i == TENTATIVE:
            body += ["## 其他", "", "- 年底大版本发布日期暂定 11 月 20 日。", ""]
        if i == DELAY:
            body += ["## 其他", "", "- 王磊：支付模块回归测试还没过，建议发布推迟。", "- 大家同意。", ""]
            conclusion = decision + " 年底大版本发布从 11 月 20 日推迟到 12 月 8 日（王磊提出，原因是支付模块回归测试未通过）。"
        if i == OUTING:
            body += ["## 其他", "", "- 团建因为台风推迟到下个月。", ""]
        body += ["## 结论", "", conclusion, ""]
        with open(os.path.join(d, name + ".md"), "w") as f:
            f.write("\n".join(body))
        written.append(name)
        day += timedelta(days=rnd.choice([7, 7, 7, 14]))
    answers["notes"] = written
    answers["note_topics"] = {name: TOPICS[i % len(TOPICS)][0] for i, name in enumerate(written)}
    answers["notes_tentative"], answers["notes_delay"], answers["notes_outing"] = written[TENTATIVE], written[DELAY], written[OUTING]


INVOICE = '''def line_total(qty, unit_price):
    return round(qty * unit_price, 2)


def total(lines, discount=0.0):
    subtotal = sum(line_total(qty, price) for qty, price in lines)
    return round(subtotal - discount, 2)
'''

INVOICE_TEST = '''import unittest

from invoice import line_total, total


class InvoiceTest(unittest.TestCase):
    def test_line_total(self):
        self.assertEqual(line_total(3, 2.5), 7.5)

    def test_total_without_discount(self):
        self.assertEqual(total([(2, 10.0), (1, 5.5)]), 25.5)

    def test_total_with_discount(self):
        self.assertEqual(total([(2, 10.0)], discount=0.1), 18.0)


if __name__ == "__main__":
    unittest.main()
'''


def tools(root, answers):
    d = os.path.join(root, "tools")
    os.makedirs(d)
    with open(os.path.join(d, "invoice.py"), "w") as f:
        f.write(INVOICE)
    with open(os.path.join(d, "test_invoice.py"), "w") as f:
        f.write(INVOICE_TEST)
    answers["invoice_test_sha256"] = hashlib.sha256(INVOICE_TEST.encode()).hexdigest()


def code(root):
    dest = os.path.join(root, "code")
    with urllib.request.urlopen(CODE_URL, timeout=120) as r:
        raw = r.read()
    with tarfile.open(fileobj=io.BytesIO(raw)) as tar:
        for m in tar.getmembers():
            parts = m.name.split("/", 1)
            if len(parts) < 2 or not parts[1]:
                continue
            m.name = parts[1]
            tar.extract(m, dest)
    shutil.rmtree(os.path.join(dest, "eval"), ignore_errors=True)
    shutil.rmtree(os.path.join(dest, "bench", "results"), ignore_errors=True)


def build(root, with_code=True):
    os.makedirs(root, exist_ok=True)
    answers = {}
    sales(root, answers)
    log(root, answers)
    notes(root, answers)
    tools(root, answers)
    if with_code:
        code(root)
    return answers


def main():
    p = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    p.add_argument("root")
    p.add_argument("--no-code", action="store_true", help="leave out ollupi's source, which is downloaded")
    a = p.parse_args()
    build(a.root, not a.no_code)


if __name__ == "__main__":
    main()
