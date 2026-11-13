"""Ollupi figures: every README image in one visual system.

Each figure is 1600 wide on white. A header carries the emblem (the little wolf by the moon), an eyebrow, a title
that states the point and a line of context; a footer carries the conditions or the source. The colours are the
desktop app's stone and moss: moss is Ollupi, rose is the per-agent isolation it replaces, the wolf's tan is tool
execution, warm grey is neutral; waiting is hatched. Text never takes a series color.

Run from anywhere: python3 docs/images/figures.py. The charts' numbers are the bench's output, written in below; the
burst timeline and the soak are read from bench/results, where the raw output is kept. After a new measurement, update
both and run this again.
"""
import csv
import math
import os
import re

OUT = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(os.path.dirname(OUT))
BURST_CSV = f"{ROOT}/bench/results/burst.csv"
SOAK_CSV = f"{ROOT}/bench/results/soak.csv"
W, PAD = 1600, 72

INDIGO, INDIGO_2, INDIGO_DEEP = "#2f5d47", "#94b59f", "#22252a"  # moss, light moss, ink
TINT, TINT_2, LINE = "#eef3ec", "#dfe9db", "#c3d3bd"
ROSE, ROSE_TINT, ROSE_LINE = "#f43f5e", "#fff1f2", "#fecdd3"
PINK = "#c89a69"  # the wolf's tan
INK2, MUTED, FAINT = "#3d4148", "#5b5f66", "#8e928f"
GRID, RULE, BASE, PAPER = "#f1f0ec", "#e6e4de", "#cfccc3", "#f8f7f4"
FONT = '-apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif'
MONO = "ui-monospace, 'SF Mono', Menlo, monospace"
ENV = "Apple M1 · 8 cores · 8 GB · one SQLite database · simulated model and tools"
SOURCE = "github.com/0xfakeSpike/ollupi"

WOLF = re.search(r"<svg[^>]*>(.*)</svg>", open(f"{ROOT}/docs/logo/mark.svg").read(), re.S).group(1)


def esc(s):
    return s.replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;")


class Figure:
    def __init__(self, h, eyebrow, title, sub, foot_left, foot_right):
        self.h = h
        self.p = [
            f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 {W} {h}" width="{W}" height="{h}">',
            '<defs>'
            f'<pattern id="hatch" width="10" height="10" patternUnits="userSpaceOnUse" patternTransform="rotate(45)">'
            f'<rect width="10" height="10" fill="{PAPER}"/><line x1="0" y1="0" x2="0" y2="10" stroke="{BASE}" stroke-width="4"/></pattern>'
            f'<marker id="arrow" viewBox="0 0 10 10" refX="9" refY="5" markerUnits="userSpaceOnUse" markerWidth="12" markerHeight="12" orient="auto-start-reverse">'
            f'<path d="M0,1 L9,5 L0,9 z" fill="{FAINT}"/></marker>'
            '</defs>',
            f'<style>text {{ font-family: {FONT}; fill: {INDIGO_DEEP}; }}'
            f' .eyebrow {{ font-size: 16px; font-weight: 600; letter-spacing: 3px; fill: {INDIGO}; }}'
            f' .title {{ font-size: 36px; font-weight: 700; }}'
            f' .sub {{ font-size: 19px; fill: {MUTED}; }}'
            f' .kpi {{ font-size: 44px; font-weight: 700; letter-spacing: -0.5px; }}'
            f' .unit {{ font-size: 21px; font-weight: 600; fill: {INK2}; }}'
            f' .cap {{ font-size: 17px; fill: {MUTED}; }}'
            f' .h {{ font-size: 22px; font-weight: 700; }}'
            f' .hs {{ font-size: 19px; font-weight: 600; }}'
            f' .b {{ font-size: 17px; fill: {INK2}; }}'
            f' .s {{ font-size: 15px; fill: {MUTED}; }}'
            f' .code {{ font-family: {MONO}; font-weight: 700; fill: {INDIGO}; }}'
            f' .tick {{ font-size: 16px; fill: {MUTED}; }}'
            f' .lab {{ font-size: 17px; fill: {INK2}; }}'
            f' .val {{ font-size: 17px; font-weight: 600; }}'
            f' .chip {{ font-size: 15px; font-weight: 600; }}'
            f' .foot {{ font-size: 15px; fill: {FAINT}; }}</style>',
            f'<rect width="{W}" height="{h}" fill="#ffffff"/>',
            f'<svg x="{PAD}" y="40" width="68" height="68" viewBox="0 0 100 100">{WOLF}</svg>',
            f'<text class="eyebrow" x="{PAD + 92}" y="62">{esc(eyebrow)}</text>',
            f'<text class="title" x="{PAD + 92}" y="104">{esc(title)}</text>',
            f'<text class="sub" x="{PAD}" y="152">{esc(sub)}</text>',
            f'<line x1="{PAD}" y1="{h - 60}" x2="{W - PAD}" y2="{h - 60}" stroke="{RULE}" stroke-width="2"/>',
            f'<text class="foot" x="{PAD}" y="{h - 28}">{esc(foot_left)}</text>',
            f'<text class="foot" x="{W - PAD}" y="{h - 28}" text-anchor="end" style="font-family: {MONO}">{esc(foot_right)}</text>',
        ]

    def add(self, s):
        self.p.append(s)

    def text(self, x, y, s, cls, anchor="start", style=""):
        st = f' style="{style}"' if style else ""
        self.p.append(f'<text class="{cls}" x="{x:.1f}" y="{y:.1f}" text-anchor="{anchor}"{st}>{esc(s)}</text>')

    def card(self, x, y, w, h, fill="#ffffff", stroke=LINE, r=14, dash=False, sw=2):
        d = ' stroke-dasharray="8 6"' if dash else ""
        self.p.append(f'<rect x="{x}" y="{y}" width="{w}" height="{h}" rx="{r}" fill="{fill}" stroke="{stroke}" stroke-width="{sw}"{d}/>')

    def block(self, x, y, w, h, fill, label="", ink="#ffffff", r=8):
        self.p.append(f'<rect x="{x:.1f}" y="{y}" width="{w:.1f}" height="{h}" rx="{r}" fill="{fill}"/>')
        if label:
            self.text(x + w / 2, y + h / 2 + 6, label, "chip", "middle", f"fill:{ink}")

    def arrow(self, d, dash=False):
        st = ' stroke-dasharray="7 6"' if dash else ""
        self.p.append(f'<path d="{d}" fill="none" stroke="{FAINT}" stroke-width="2" marker-end="url(#arrow)"{st}/>')

    def kpis(self, items, y=196):
        width = (W - 2 * PAD) / len(items)
        self.add(f'<rect x="{PAD}" y="{y}" width="{W - 2 * PAD}" height="124" rx="16" fill="{TINT}"/>')
        for i, (value, unit, caption) in enumerate(items):
            x = PAD + i * width + 32
            if i:
                self.add(f'<line x1="{PAD + i * width}" y1="{y + 26}" x2="{PAD + i * width}" y2="{y + 98}" stroke="{LINE}" stroke-width="2"/>')
            self.add(f'<text x="{x}" y="{y + 66}"><tspan class="kpi">{esc(value)}</tspan><tspan class="unit" dx="8">{esc(unit)}</tspan></text>')
            self.text(x, y + 100, caption, "cap")

    def legend(self, x, y, entries, bars=False):
        """Swatches right-aligned to x: (color, label, dashed)."""
        cx = x
        for color, label, dashed in reversed(entries):
            cx -= len(label) * 17 * 0.56
            self.text(cx, y + 6, label, "lab")
            if bars:
                cx -= 28
                self.add(f'<rect x="{cx}" y="{y - 9}" width="18" height="18" rx="4" fill="{color}"/>')
                cx -= 28
                continue
            cx -= 42
            d = ' stroke-dasharray="8 6"' if dashed else ""
            self.add(f'<line x1="{cx}" y1="{y}" x2="{cx + 30}" y2="{y}" stroke="{color}" stroke-width="{3 if dashed else 5}" stroke-linecap="round"{d}/>')
            cx -= 28

    def save(self, name):
        with open(f"{OUT}/{name}", "w") as f:
            f.write("\n".join(self.p + ["</svg>"]) + "\n")


# ---------- diagrams ----------

def architecture():
    f = Figure(960, "OLLUPI · ARCHITECTURE", "One process hosts every session",
                "Sessions, subagents and scheduled tasks share one agent's scheduler, and all state lives in SQLite", "Every step is written to the database before it runs", SOURCE)
    # clients and gateway
    f.card(560, 196, 480, 60, TINT, LINE)
    f.text(800, 233, "ollupi-desktop · other clients", "hs", "middle")
    f.arrow("M800,258 V300")
    f.arrow("M800,300 V258")
    f.text(816, 284, "JSON-RPC 2.0 · WebSocket", "s")
    f.card(360, 302, 880, 64)
    f.text(392, 342, "gateway", "h code")
    f.text(510, 341, "Request dispatch · event push · contract and method table from one source", "b")
    f.arrow("M800,368 V404")
    # agent
    f.card(330, 406, 940, 236, TINT, INDIGO, r=18)
    f.text(360, 444, "agent", "h code")
    mods = [("Scheduler", ["Shared pool, 32 slots by default", "Slots released while waiting", "Sessions before subagents"]),
            ("runtime", ["Re-reads DB before model calls", "Model call → tool execution", "Steps written before they run"]),
            ("Permissions · redaction", ["Three layers of permission rules", "Seatbelt sandbox", "Secrets replaced before sending"])]
    for i, (h, lines) in enumerate(mods):
        x = 360 + i * 300
        f.card(x, 466, 280, 156)
        f.text(x + 22, 504, h, "hs code" if h == "runtime" else "hs")
        for j, line in enumerate(lines):
            f.text(x + 22, 540 + j * 26, line, "b")
    # side services
    for i, (h, lines) in enumerate([("Memory", ["Distilled from ended turns", "Global · general · workspace"]),
                                     ("Scheduled tasks", ["Sends a message when due", "Queues with other turns"])]):
        y = 420 + i * 116
        f.card(72, y, 228, 100)
        f.text(94, y + 36, h, "hs")
        for j, line in enumerate(lines):
            f.text(94, y + 64 + j * 24, line, "s")
        f.arrow(f"M300,{y + 50} H326")
    f.card(1300, 420, 228, 100)
    f.text(1322, 456, "Model connections", "hs")
    f.text(1322, 484, "OpenAI · Anthropic · Gemini", "s")
    f.text(1322, 508, "Concurrency per connection", "s")
    f.arrow("M1272,470 H1296")
    f.card(1300, 552, 228, 90, PAPER, BASE, dash=True)
    f.text(1322, 590, "Model providers", "hs")
    f.text(1322, 618, "Receive placeholders only", "s")
    f.arrow("M1414,522 V548")
    # tools
    f.arrow("M800,644 V680")
    f.text(816, 668, "Called after permission checks", "s")
    tools = [("Built-in tools", "Subagent · plan · ask"), ("run_command", "Runs in the sandbox"), ("MCP servers", "stdio · HTTP"), ("App tools", "tools.Provider")]
    for i, (h, s) in enumerate(tools):
        x = 360 + i * 226
        f.card(x, 682, 206, 82)
        f.text(x + 103, 716, h, "hs code" if h == "run_command" else "hs", "middle")
        f.text(x + 103, 744, s, "s", "middle")
    # SQLite
    f.card(72, 796, 1456, 92, PAPER, BASE, r=16)
    f.add(f'<g transform="translate(100,816)" fill="none" stroke="{INDIGO}" stroke-width="3"><ellipse cx="22" cy="10" rx="22" ry="8"/>'
          f'<path d="M0,10 V44 A22,8 0 0 0 44,44 V10"/><path d="M0,27 A22,8 0 0 0 44,27"/></g>')
    f.text(166, 836, "SQLite", "h")
    f.text(166, 866, "thread · turn · item · memory · scheduled tasks · permission rules · message full-text index", "b")
    f.text(1500, 836, "Write first, then run", "hs", "end")
    f.text(1500, 866, "The queue and in-progress turns recover after a restart", "b", "end")
    f.arrow("M186,638 V792", dash=True)
    f.arrow("M800,766 V792")
    f.save("architecture.svg")


def comparison():
    f = Figure(940, "OLLUPI · COMPARISON", "The same machine hosts more sessions",
                "Isolated, a waiting agent still holds a process; Ollupi releases the slot while waiting, and another turn takes over", "Letters are sessions; hatching is waiting", SOURCE)
    lanes_x, lane_w = 236, 520

    def lane(x0, y, label, segs, color):
        f.text(x0 - 20, y + 30, label, "lab", "end")
        x = x0
        for kind, w, text in segs:
            ww = w * lane_w
            if kind == "run":
                f.block(x, y + 4, ww - 6, 44, color, text)
            elif kind == "wait":
                f.add(f'<rect x="{x:.1f}" y="{y + 4}" width="{ww - 6:.1f}" height="44" rx="8" fill="url(#hatch)" stroke="{BASE}" stroke-width="1.5"/>')
                f.text(x + (ww - 6) / 2, y + 32, text, "s", "middle")
            x += ww

    for side, x0, title, sub, color, tint, line in [
            (0, 72, "Per-agent isolation", "One VM or container per agent", ROSE, ROSE_TINT, ROSE_LINE),
            (1, 816, "Ollupi", "One process, one shared execution pool", INDIGO, TINT, LINE)]:
        f.card(x0, 196, 712, 600, tint, line, r=18)
        f.text(x0 + 32, 242, title, "h")
        f.text(x0 + 32, 272, sub, "s")
    lx = 72 + 164
    lane(lx, 300, "Process 1", [("run", .28, "A"), ("wait", .44, "awaiting approval"), ("run", .28, "A")], ROSE)
    lane(lx, 364, "Process 2", [("run", .2, "B"), ("wait", .3, "awaiting answer"), ("run", .22, "B"), ("wait", .28, "awaiting approval")], ROSE)
    lane(lx, 428, "Process 3", [("run", .36, "C"), ("wait", .38, "awaiting answer"), ("run", .26, "C")], ROSE)
    f.text(lx - 20, 522, "Sessions D, E", "lab", "end")
    f.card(lx, 496, lane_w - 6, 44, "#ffffff", ROSE_LINE, r=8, dash=True)
    f.text(lx + (lane_w - 6) / 2, 524, "Each needs a new process", "s", "middle")
    rx = 816 + 164
    lane(rx, 300, "Slot 1", [("run", .2, "A"), ("run", .22, "D"), ("run", .2, "Scheduled"), ("run", .2, "A"), ("run", .18, "E")], INDIGO)
    lane(rx, 364, "Slot 2", [("run", .2, "B"), ("run", .38, "E"), ("run", .2, "B"), ("run", .22, "D")], INDIGO)
    lane(rx, 428, "Slot 3", [("run", .3, "C"), ("run", .44, "D's subagent"), ("run", .26, "C")], INDIGO)
    f.text(rx - 20, 516, "Waiting", "lab", "end")
    f.text(rx - 20, 538, "No slot held", "s", "end")
    for i, (x, y, t) in enumerate([(0, 496, "A awaits approval"), (.34, 496, "B awaits answer"), (.68, 496, "B awaits approval"), (.17, 534, "C awaits answer")]):
        f.add(f'<rect x="{rx + x * lane_w:.1f}" y="{y}" width="150" height="30" rx="8" fill="url(#hatch)" stroke="{BASE}" stroke-width="1.5"/>')
        f.text(rx + x * lane_w + 75, y + 21, t, "s", "middle")
    for side, x0, items in [(0, 72, ["One process hosts only one session", "A waiting process sits suspended, still holding memory", "Scheduling state is in memory, lost when the process exits", "Scheduled tasks need an external cron to start a process"]),
                            (1, 816, ["One process runs every session, subagent and scheduled task", "A waiting turn releases its slot and another turn takes over", "Scheduling state is in SQLite; a restart resumes where it stopped", "Scheduled tasks message a session when due and share the queue"])]:
        f.add(f'<line x1="{x0 + 32}" y1="608" x2="{x0 + 680}" y2="608" stroke="{ROSE_LINE if side == 0 else LINE}" stroke-width="2"/>')
        for i, t in enumerate(items):
            f.add(f'<rect x="{x0 + 34}" y="{640 + i * 36 - 7}" width="8" height="8" fill="{ROSE if side == 0 else INDIGO}"/>')
            f.text(x0 + 56, 640 + i * 36, t, "b")
    f.save("cli-vs-ollupi.svg")


def pool():
    f = Figure(940, "OLLUPI · EXECUTION POOL", "Every turn shares one execution pool",
                "Waiting turns release their slots, and model concurrency goes only to turns calling the model", "An agent spends most of its time waiting on the model; the pool keeps as many model calls in flight as it can", SOURCE)
    for i, (h, s) in enumerate([("User messages", "Any session"), ("Subagents", "Plain sessions and turns"), ("Scheduled tasks", "Sends a message when due")]):
        y = 236 + i * 118
        f.card(72, y, 228, 90)
        f.text(96, y + 38, h, "hs")
        f.text(96, y + 66, s, "s")
        f.arrow(f"M300,{y + 45} C340,{y + 45} 340,{392} 372,{392}")
    # queue
    f.card(376, 212, 300, 380, TINT, LINE, r=18)
    f.text(400, 252, "Queue", "h")
    f.text(400, 280, "Runnable turns in the database", "s")
    for i, t in enumerate(["Session A · turn 3", "Session B · turn 1", "Subagent A-1", "Subagent A-2", "Scheduled · daily digest"]):
        f.card(400, 300 + i * 54, 252, 42, "#ffffff", LINE, r=10)
        f.text(418, 327 + i * 54, t, "b")
    f.text(376, 618, "When slots are full, sessions run", "s")
    f.text(376, 642, "before subagents; within each kind,", "s")
    f.text(376, 666, "turns run in ready order", "s")
    f.arrow("M678,402 H716")
    # slots
    f.card(720, 212, 420, 380, "#ffffff", INDIGO, r=18)
    f.text(744, 252, "Slots", "h")
    f.text(744, 280, "32 by default; one per turn, released when waiting", "s")
    import random
    random.seed(7)
    states = ["model"] * 13 + ["tool"] * 7 + ["idle"] * 12
    random.shuffle(states)
    for i, st in enumerate(states):
        r, c = divmod(i, 8)
        x, y = 744 + c * 46, 304 + r * 46
        if st == "idle":
            f.add(f'<rect x="{x}" y="{y}" width="38" height="38" rx="8" fill="{PAPER}" stroke="{RULE}" stroke-width="2"/>')
        else:
            f.add(f'<rect x="{x}" y="{y}" width="38" height="38" rx="8" fill="{INDIGO if st == "model" else PINK}"/>')
    for i, (c, t) in enumerate([(INDIGO, "Model call"), (PINK, "Tool call"), (None, "Idle")]):
        x = 744 + i * 128
        if c:
            f.add(f'<rect x="{x}" y="{514}" width="16" height="16" rx="4" fill="{c}"/>')
        else:
            f.add(f'<rect x="{x}" y="{514}" width="16" height="16" rx="4" fill="{PAPER}" stroke="{RULE}" stroke-width="2"/>')
        f.text(x + 24, 528, t, "s")
    f.text(744, 566, "Tool calls hold a slot but no model concurrency", "s")
    f.arrow("M1142,402 H1180")
    # model concurrency
    f.card(1184, 212, 344, 380, "#ffffff", LINE, r=18)
    f.text(1208, 252, "Model concurrency", "h")
    f.text(1208, 280, "Per connection, 4 by default", "s")
    for j, conn in enumerate(["Connection A", "Connection B"]):
        y = 318 + j * 120
        f.text(1208, y, conn, "lab")
        for k in range(4):
            f.add(f'<rect x="{1208 + k * 76}" y="{y + 16}" width="66" height="40" rx="8" fill="{INDIGO}"/>')
    f.text(1208, 566, "When full, calls queue within quota", "s")
    # waiting
    f.arrow("M930,594 V676")
    f.text(946, 640, "Needs an approval, an answer or a subagent", "s")
    f.add(f'<rect x="720" y="680" width="420" height="156" rx="18" fill="url(#hatch)" stroke="{BASE}" stroke-width="2"/>')
    f.add(f'<rect x="740" y="696" width="380" height="124" rx="12" fill="#ffffff"/>')
    f.text(762, 732, "Waiting", "h")
    f.text(762, 760, "Holds no slot, goroutine or model concurrency", "s")
    for i, t in enumerate(["approval", "answer", "subagent"]):
        f.add(f'<rect x="{762 + i * 116}" y="778" width="104" height="28" rx="14" fill="{TINT}"/>')
        f.text(762 + i * 116 + 52, 797, t, "chip", "middle", f"fill:{INDIGO}")
    f.arrow("M718,758 H652 V596")
    f.text(636, 700, "Ready again once the answer, approval", "s", "end")
    f.text(636, 722, "or subagent result arrives", "s", "end")
    f.save("execution-pool.svg")


# ---------- charts ----------

class Plot:
    def __init__(self, fig, x, y, w, h, xdom, ydom, xlog=False, ylog=False):
        self.f, self.x, self.y, self.w, self.h = fig, x, y, w, h
        self.xdom, self.ydom, self.xlog, self.ylog = xdom, ydom, xlog, ylog

    def _t(self, v, dom, log):
        a, b = dom
        if log:
            v, a, b = math.log10(v), math.log10(a), math.log10(b)
        return (v - a) / (b - a)

    def px(self, v):
        return self.x + self._t(v, self.xdom, self.xlog) * self.w

    def py(self, v):
        return self.y + self.h - self._t(v, self.ydom, self.ylog) * self.h

    def frame(self, title, unit, yticks, xticks, xlabel):
        f = self.f
        f.text(self.x, self.y - 48, title, "hs")
        f.text(self.x, self.y - 20, unit, "tick")
        for v, label in yticks:
            yy = self.py(v)
            f.add(f'<line x1="{self.x}" y1="{yy:.1f}" x2="{self.x + self.w}" y2="{yy:.1f}" stroke="{GRID}" stroke-width="2"/>')
            f.text(self.x - 14, yy + 6, label, "tick", "end")
        f.add(f'<line x1="{self.x}" y1="{self.y + self.h}" x2="{self.x + self.w}" y2="{self.y + self.h}" stroke="{BASE}" stroke-width="2"/>')
        for v, label in xticks:
            f.text(self.px(v), self.y + self.h + 30, label, "tick", "middle")
        f.text(self.x + self.w / 2, self.y + self.h + 64, xlabel, "cap", "middle")

    def line(self, pts, color, width=4, dashed=False):
        d = " ".join(f"{self.px(a):.1f},{self.py(b):.1f}" for a, b in pts)
        dash = ' stroke-dasharray="10 8"' if dashed else ""
        self.f.add(f'<polyline points="{d}" fill="none" stroke="{color}" stroke-width="{width}" stroke-linejoin="round" stroke-linecap="round"{dash}/>')

    def dots(self, pts, color):
        for a, b in pts:
            self.f.add(f'<circle cx="{self.px(a):.1f}" cy="{self.py(b):.1f}" r="7" fill="{color}" stroke="#ffffff" stroke-width="3"/>')

    def label(self, a, b, s, cls="lab", anchor="start", dx=0, dy=0):
        self.f.text(self.px(a) + dx, self.py(b) + dy, s, cls, anchor)

    def bar(self, cx, v, width, color):
        x0, x1, top, base = cx - width / 2, cx + width / 2, self.py(v), self.py(self.ydom[0])
        self.f.add(f'<path d="M{x0:.1f},{base:.1f} V{top + 5:.1f} Q{x0:.1f},{top:.1f} {x0 + 5:.1f},{top:.1f} '
                   f'H{x1 - 5:.1f} Q{x1:.1f},{top:.1f} {x1:.1f},{top + 5:.1f} V{base:.1f} Z" fill="{color}"/>')


def size(mb):
    if mb >= 1024 * 1024:
        return f"{mb / 1024 / 1024:g} TB"
    if mb >= 1024:
        return f"{mb / 1024:g} GB"
    return f"{mb:g} MB"


CHART_H = 1000


def memory():
    f = Figure(CHART_H, "OLLUPI BENCH · MEMORY", "A parked session lives in the database, not in process memory",
               "Resident memory (RSS) by session count, log-log. The control is one Ollupi process per session, not counting container overhead",
               ENV, "go run ./bench parked | steady | isolated")
    f.kpis([("< 0.3", "KB", "per parked session (measured at 50,000 parked)"),
            ("≈ 160", "KB", "per active session, including its HTTP connection"),
            ("20.0", "MB", "control: one process per session")])
    p = Plot(f, 190, 470, 1250, 360, (100, 100_000), (10, 2 * 1024 * 1024), xlog=True, ylog=True)
    p.frame("Process memory", "RSS, log scale", [(v, size(v)) for v in (10, 100, 1024, 10 * 1024, 100 * 1024, 1024 * 1024)],
            [(v, f"{v:,}") for v in (100, 1000, 10_000, 100_000)], "Sessions, log scale")
    p.line([(100, 2000), (100_000, 2_000_000)], ROSE)
    p.label(30_000, 600_000, "One process per session", "lab", "end", dx=-14, dy=-14)
    active = [(512, 132), (1024, 223), (1536, 315), (2048, 407), (3072, 570), (4096, 692)]
    p.line(active, INDIGO)
    p.dots(active, INDIGO)
    p.label(4096, 692, "Ollupi · active sessions", "lab", "start", dx=18, dy=6)
    waiting = [(1000, 28.4), (10_000, 38.6), (50_000, 36.8)]
    p.line(waiting, INDIGO_2)
    p.dots(waiting, INDIGO_2)
    p.label(50_000, 36.8, "Ollupi · parked sessions", "lab", "end", dy=-20)
    f.legend(W - PAD, 422, [(INDIGO, "Ollupi · active sessions", False), (INDIGO_2, "Ollupi · parked sessions", False), (ROSE, "One process per session", False)])
    f.save("bench-memory.svg")


def capacity():
    pts = [(512, 17.1, 20.4, 72), (1024, 17.2, 20.5, 147), (1536, 17.3, 20.7, 219), (2048, 17.3, 20.8, 296), (3072, 18.0, 22.5, 416), (4096, 20.9, 26.1, 477)]
    f = Figure(CHART_H, "OLLUPI BENCH · CAPACITY", "Up to about 3000 active sessions, turns run about as fast as alone",
               "Closed loop: each session starts a turn as soon as the last ends. A turn is 3 model calls (5 s ± 2 s) and 2 tool calls (1 s), about 17 s alone",
               ENV, "go run ./bench steady -c 3072")
    f.kpis([("+6%", "", "increase in turn p50 at 3072 active sessions"), ("477", "calls/s", "model call throughput at 4096 active sessions"), ("11.6", "KB", "disk per turn")])
    left = Plot(f, 160, 470, 600, 360, (0, 4400), (0, 30))
    left.frame("Turn duration", "seconds", [(v, f"{v}") for v in (0, 10, 20, 30)], [(v, f"{v:,}") for v in (1024, 2048, 3072, 4096)], "Concurrent active sessions")
    left.line([(0, 17), (4400, 17)], FAINT, width=2, dashed=True)
    left.label(4400, 17, "Alone: 17 s", "cap", "end", dy=30)
    left.line([(x[0], x[2]) for x in pts], INDIGO_2)
    left.dots([(x[0], x[2]) for x in pts], INDIGO_2)
    left.line([(x[0], x[1]) for x in pts], INDIGO)
    left.dots([(x[0], x[1]) for x in pts], INDIGO)
    left.label(4096, 26.1, "p95", "val", "start", dx=16, dy=6)
    left.label(4096, 20.9, "p50", "val", "start", dx=16, dy=6)
    f.legend(760, 422, [(INDIGO, "p50", False), (INDIGO_2, "p95", False)])
    right = Plot(f, 940, 470, 600, 360, (0, 4400), (0, 500))
    right.frame("Model call throughput", "calls/s", [(v, f"{v}") for v in (0, 100, 200, 300, 400, 500)], [(v, f"{v:,}") for v in (1024, 2048, 3072, 4096)], "Concurrent active sessions")
    right.line([(x[0], x[3]) for x in pts], INDIGO)
    right.dots([(x[0], x[3]) for x in pts], INDIGO)
    for x in pts:
        if x[0] in (1024, 2048, 4096):
            right.label(x[0], x[3], f"{x[3]}", "val", "middle", dy=-20)
    f.save("bench-capacity.svg")


def burst():
    rows = [(float(r["seconds"]), int(r["in_flight"]), int(r["turns_ended"])) for r in csv.DictReader(open(BURST_CSV))]
    end = max(t for t, _, _ in rows)
    f = Figure(CHART_H, "OLLUPI BENCH · BURST AND QUOTA", "Bursts queue at the quota: in-flight requests stay within it, no 429s",
               "3000 turns submitted at once; connection and provider both capped at 256 concurrent. A turn is 3 model calls (5 s ± 2 s) and 2 tool calls (1 s)",
               ENV, "go run ./bench burst -n 3000 -slots 512 -calls 256 -latency 5s")
    f.kpis([("256", "/ 256", "peak in-flight requests / quota"), ("0", "", "429s from the provider"), ("181.5", "s", "to finish all; theoretical floor 175.8 s")])
    xt = [(v, f"{v}") for v in range(0, int(end) + 1, 30)]
    left = Plot(f, 160, 470, 600, 360, (0, end), (0, 300))
    left.frame("In-flight model requests", "requests", [(v, f"{v}") for v in (0, 100, 200, 300)], xt, "seconds")
    left.line([(0, 256), (end, 256)], FAINT, width=2, dashed=True)
    left.label(end, 256, "Quota 256", "cap", "end", dy=-12)
    left.line([(0, 0)] + [(t, x) for t, x, _ in rows], INDIGO)
    right = Plot(f, 940, 470, 600, 360, (0, end), (0, 3000))
    right.frame("Turns completed", "turns", [(v, f"{v:,}") for v in (0, 1000, 2000, 3000)], xt, "seconds")
    right.line([(0, 0), (175.8, 3000)], FAINT, width=2, dashed=True)
    right.line([(0, 0)] + [(t, e) for t, _, e in rows], INDIGO)
    f.legend(W - PAD, 422, [(INDIGO, "Measured", False), (FAINT, "Theoretical floor", True)])
    f.save("bench-burst.svg")


def recovery():
    f = Figure(CHART_H, "OLLUPI BENCH · RECOVERY", "Process restarts and session wake-ups take milliseconds",
               "Wake-up: from messaging a session idle in the database to its model request going out. Crash: 90 random kill -9s under mixed load",
               ENV, "go run ./bench resume | crash")
    f.kpis([("0", "/ 960", "turns lost to kill -9"), ("0", "", "tool calls run twice"), ("448", "ms", "restart: 10,000 parked + 500 running turns")])
    data = [("10 turns", 5.2, 6.6), ("100 turns", 18.8, 22.5), ("1000 turns", 73.2, 150.4)]
    p = Plot(f, 160, 470, 1368, 360, (0, 3), (0, 160))
    p.frame("Session wake-up time", "ms", [(v, f"{v}") for v in (0, 40, 80, 120, 160)], [(i + 0.5, d[0]) for i, d in enumerate(data)], "Session history length")
    for i, (_, p50, p95) in enumerate(data):
        cx = p.px(i + 0.5)
        p.bar(cx - 52, p50, 92, INDIGO)
        p.bar(cx + 52, p95, 92, INDIGO_2)
        f.text(cx - 52, p.py(p50) - 14, f"{p50:g}", "val", "middle")
        f.text(cx + 52, p.py(p95) - 14, f"{p95:g}", "val", "middle")
    f.legend(W - PAD, 422, [(INDIGO, "p50", False), (INDIGO_2, "p95", False)], bars=True)
    f.save("bench-recovery.svg")


def hbars(f, x, y, w, title, unit, rows, top):
    """A small panel of horizontal bars: (name, value, label, color), the longest bar standing for top."""
    f.text(x, y, title, "hs")
    f.text(x, y + 28, unit, "tick")
    bx, bw = x + 190, w - 190 - 130
    for i, (name, v, label, color) in enumerate(rows):
        yy = y + 58 + i * 62
        width = max(bw * v / top, 6)
        f.text(x, yy + 27, name, "lab")
        f.block(bx, yy, width, 40, color, r=6)
        f.text(bx + width + 14, yy + 27, label, "val")


def compare():
    f = Figure(CHART_H, "OLLUPI BENCH · COMPARISON", "Lowest parked memory, bursts complete, turns resume after kill -9",
               "Same simulated provider and load, one run each. Memory: resident memory for Ollupi, phys_footprint for the others",
               "pi-coding-agent 1.4.0 · pi-durable 1.4.0 (experimental) · DeepSeek Harness 0.2.1-rc.5 · " + ENV, "bench/compare")
    names = ["Ollupi", "pi (SDK)", "pi-durable", "DeepSeek Harness"]

    def rows(values, labels):
        return [(n, v, l, INDIGO if i == 0 else BASE) for i, (n, v, l) in enumerate(zip(names, values, labels))]

    hbars(f, PAD, 210, 690, "Memory per parked session", "KB, lower is better", rows([0.3, 91, 23, 181], ["0.3", "91", "23", "181"]), 181)
    hbars(f, 838, 210, 690, "Increase in turn p50 at 2048 active sessions", "Lower is better", rows([2, 2, 314, 77], ["2%", "2%", "314%", "77%"]), 314)
    hbars(f, PAD, 570, 690, "Turns completed, 3000 submitted at once with quota 256", "Turns, higher is better",
          rows([3000, 466, 819, 810], ["3000", "466", "819", "810"]), 3000)
    f.text(838, 570, "After kill -9 mid-run", "hs")
    f.text(838, 598, "Mixed load, repeated restarts", "tick")
    outcomes = [("All resume, no tool runs twice", True), ("In-progress turns lost", False), ("All resume, no tool runs twice", True), ("Turns marked interrupted", False)]
    for i, (name, (outcome, kept)) in enumerate(zip(names, outcomes)):
        yy = 628 + i * 62
        f.text(838, yy + 27, name, "lab")
        f.block(838 + 190, yy, 500, 40, TINT_2 if kept else GRID, outcome, INDIGO_DEEP if kept else INK2, r=6)
    f.save("bench-compare.svg")


def mix():
    f = Figure(CHART_H, "OLLUPI BENCH · MIXED LOAD", "Scheduled peaks queue at the model quota; off-peak turns barely wait",
               "256 sessions send turns (20% to subagents) while 1000 scheduled tasks come due at once; quota 256. Fan-out: 64 turns × 8 subagents, 32 slots",
               ENV, "go run ./bench mix | fanout")
    f.kpis([("274", "ms", "off-peak wait p99"), ("59.4", "s", "to drain 1000 scheduled tasks"), ("0.978", "", "Jain fairness index across sessions"), ("1.11", "×", "fan-out time / theoretical floor")])
    data = [("User turns · off-peak", 7, 274), ("Subagents", 5, 4571), ("User turns · during peak", 44107, 58608), ("Scheduled tasks", 29637, 58276)]
    p = Plot(f, 160, 470, 1368, 360, (0, 4), (1, 100_000), ylog=True)
    ticks = [(1, "1 ms"), (10, "10 ms"), (100, "100 ms"), (1000, "1 s"), (10_000, "10 s"), (100_000, "100 s")]
    p.frame("Wait for the first model request", "Log scale", ticks, [(i + 0.5, d[0]) for i, d in enumerate(data)], "Turn type")
    for i, (_, p50, p99) in enumerate(data):
        cx = p.px(i + 0.5)
        for dx, v, color in ((-52, p50, INDIGO), (52, p99, INDIGO_2)):
            p.bar(cx + dx, v, 92, color)
            f.text(cx + dx, p.py(v) - 14, f"{v} ms" if v < 1000 else f"{v / 1000:.1f} s", "val", "middle")
    f.legend(W - PAD, 422, [(INDIGO, "p50", False), (INDIGO_2, "p99", False)], bars=True)
    f.save("bench-mix.svg")


def faults():
    f = Figure(CHART_H, "OLLUPI BENCH · FAULTS AND LONG TAIL", "When model requests fail or slow down, turns slow but don't fail",
               "Faults: 5% of requests return 500, other clients on the account take up to 256 of the quota. Long tail: log-normal model latency, median 5 s, σ = 0.6",
               ENV, "go run ./bench steady -fail 0.05 -others 256 | -sigma 0.6")
    f.kpis([("0", "/ 3072", "turns failed under faults"), ("326 → 0", "", "failed turns before and after backoff retries"), ("0", "/ 4608", "turns failed under the long tail")])
    data = [("No faults · 1024 sessions", 17.2, 20.5, 21.6), ("Faults · 1024 sessions", 17.8, 23.3, 28.5), ("Long tail · 1536 sessions", 19.0, 33.0, 42.4)]
    p = Plot(f, 160, 470, 1368, 360, (0, 3), (0, 50))
    p.frame("Turn duration", "seconds", [(v, f"{v}") for v in (0, 10, 20, 30, 40, 50)], [(i + 0.5, d[0]) for i, d in enumerate(data)], "Load")
    for i, (_, *values) in enumerate(data):
        cx = p.px(i + 0.5)
        for dx, v, color in zip((-104, 0, 104), values, (INDIGO, INDIGO_2, LINE)):
            p.bar(cx + dx, v, 92, color)
            f.text(cx + dx, p.py(v) - 14, f"{v:.1f}", "val", "middle")
    f.legend(W - PAD, 422, [(INDIGO, "p50", False), (INDIGO_2, "p95", False), (LINE, "p99", False)], bars=True)
    f.save("bench-faults.svg")


def soak():
    rows = list(csv.DictReader(open(SOAK_CSV)))
    history = [int(r["history_turns"]) for r in rows]
    f = Figure(CHART_H, "OLLUPI BENCH · LONG RUN", "Goroutines and WAL stay flat; CPU and memory grow with session history",
               "256 sessions run for 8 minutes, 35,000 turns in all, each lengthening its session's history. Model calls 1 s ± 0.2 s, tool calls 0.2 s",
               ENV, "go run ./bench soak -c 256 -d 8m -every 30s -latency 1s -jitter 200ms -tool 200ms")
    f.kpis([("≈ 1000", "", "goroutines, steady throughout"), ("6.1", "MB", "WAL, steady throughout"), ("0.23", "ms", "highest GC pause p99")])
    xt = [(v, f"{v}") for v in (0, 40, 80, 120)]
    left = Plot(f, 160, 470, 600, 360, (0, 145), (0, 40))
    left.frame("CPU per turn", "ms", [(v, f"{v}") for v in (0, 10, 20, 30, 40)], xt, "Session history, turns")
    cpu = list(zip(history, (float(r["cpu_ms_per_turn"]) for r in rows)))
    left.line(cpu, INDIGO)
    left.label(*cpu[0], f"{cpu[0][1]:g}", "val", "start", dy=-16)
    left.label(*cpu[-1], f"{cpu[-1][1]:g}", "val", "end", dy=-16)
    right = Plot(f, 940, 470, 600, 360, (0, 145), (0, 400))
    right.frame("Resident memory", "MB", [(v, f"{v}") for v in (0, 100, 200, 300, 400)], xt, "Session history, turns")
    rss = list(zip(history, (float(r["rss_mb"]) for r in rows)))
    right.line(rss, INDIGO)
    right.label(*rss[0], f"{rss[0][1]:g}", "val", "start", dy=-16)
    right.label(*rss[-1], f"{rss[-1][1]:g}", "val", "end", dx=-14, dy=6)
    f.save("bench-soak.svg")


architecture()
comparison()
pool()
memory()
capacity()
burst()
recovery()
compare()
mix()
faults()
soak()
