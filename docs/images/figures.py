"""Ollupi figures: every README image in one visual system.

Each figure is 1600 wide and drawn in the desktop app's dusk theme (packages/ui/src/theme.css, [data-theme='dusk'],
light): paper with the evening's light seeping in at the corners — lavender, peach, mist blue — and a fine grain. A
header carries the dusk emblem (the little wolf in the evening sky), an eyebrow, a title that states the point and a
line of context; a footer carries the conditions or the source. Dusk indigo is Ollupi, sunset is the per-agent
isolation it replaces or the control that loses, the wolf's coat is tool execution, lavender grey is neutral; waiting is
hatched. Text never takes a series color.

The emblem is the brand's, read from the desktop app's repository (assets/brand/mark-dusk.svg), checked out beside this
one or wherever OLLUPI_DESKTOP points; the figures written here carry their own copy of it.

Run from anywhere: python3 docs/images/figures.py. The charts' numbers are the bench's output (bench/results and
bench/compare/results), written in below; the soak is read from bench/results/soak.csv. After a new measurement,
update both and run this again.
"""
import csv
import math
import os
import re

OUT = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(os.path.dirname(OUT))
SOAK_CSV = f"{ROOT}/bench/results/soak.csv"
W, PAD = 1600, 72

INDIGO, INDIGO_2, INDIGO_DEEP = "#3d3768", "#a69dd4", "#26222f"
TINT, TINT_2, LINE = "#efeaf7", "#dcd2ee", "#c9bfe2"
ROSE, ROSE_TINT, ROSE_LINE = "#e0714a", "#f8e0d6", "#efc0ab"
PINK = "#d4925a"
INK2, MUTED, FAINT = "#514b5c", "#696373", "#9a93a6"
GRID, RULE, BASE, PAPER = "#ece7ee", "#e2dce6", "#cdc6d8", "#f6f2f3"
SHEET = "#fbf9f6"
FONT = '-apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif'
MONO = "ui-monospace, 'SF Mono', Menlo, monospace"
ENV = "Ollupi v0.18.0 · Apple M1 · 8 cores · 8 GB · one SQLite database · simulated model and tools"
SOURCE = "github.com/0xfakeSpike/ollupi"

DESKTOP = os.environ.get("OLLUPI_DESKTOP", os.path.join(os.path.dirname(ROOT), "ollupi-desktop"))
WOLF = re.search(r"<svg[^>]*>(.*)</svg>", open(f"{DESKTOP}/assets/brand/mark-dusk.svg").read(), re.S).group(1)


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
            '<radialGradient id="glow-lavender"><stop offset="0" stop-color="#c4b2ec" stop-opacity=".55"/><stop offset="1" stop-color="#c4b2ec" stop-opacity="0"/></radialGradient>'
            '<radialGradient id="glow-peach"><stop offset="0" stop-color="#f7cdba" stop-opacity=".6"/><stop offset="1" stop-color="#f7cdba" stop-opacity="0"/></radialGradient>'
            '<radialGradient id="glow-mist"><stop offset="0" stop-color="#bed0ee" stop-opacity=".5"/><stop offset="1" stop-color="#bed0ee" stop-opacity="0"/></radialGradient>'
            '<filter id="grain" x="0" y="0" width="100%" height="100%"><feTurbulence type="fractalNoise" baseFrequency=".95" numOctaves="3" stitchTiles="stitch"/>'
            '<feColorMatrix values="0 0 0 0 .2 0 0 0 0 .16 0 0 0 0 .28 0 0 0 1.1 -.35"/></filter>'
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
            f'<rect width="{W}" height="{h}" fill="{SHEET}"/>',
            f'<ellipse cx="0" cy="{h}" rx="{W * 0.55}" ry="{h * 0.7}" fill="url(#glow-lavender)"/>',
            f'<ellipse cx="{W}" cy="0" rx="{W * 0.5}" ry="{h * 0.6}" fill="url(#glow-peach)"/>',
            f'<ellipse cx="{W}" cy="{h}" rx="{W * 0.4}" ry="{h * 0.55}" fill="url(#glow-mist)"/>',
            f'<rect width="{W}" height="{h}" filter="url(#grain)" opacity=".28"/>',
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



def architecture():
    f = Figure(1370, "OLLUPI · ARCHITECTURE", "One write path, one feed, every session in one process",
               "Every change is one transaction through one writer; after it commits, the store tells its feed, and each listener reads the store again",
               "Events only wake: whoever hears one reads the store, so a missed event loses no work", SOURCE)
    f.card(560, 196, 480, 56, TINT, LINE)
    f.text(800, 231, "ollupi-desktop · other clients", "hs", "middle")
    f.arrow("M800,254 V296")
    f.arrow("M800,296 V254")
    f.text(816, 280, "JSON-RPC 2.0 over WebSocket · paths of its own for attachments, uploads, files and terminals", "s")
    f.card(72, 300, 1456, 92)
    f.text(100, 340, "gateway", "h code")
    f.text(220, 338, "Authenticate names a user the store holds, or the operator · one inbox in and one outbox out per connection", "b")
    f.text(220, 368, "Reads run side by side, 8 per connection; everything else in arrival order · every notification carries its commit's seq", "s")
    writers = [("run.Runner", ["Model replies and tool results", "Deltas go straight to the feed"]),
               ("app", ["Client commands: send, decide,", "answer, interrupt, hand off …"]),
               ("cron", ["Due jobs become messages,", "queued like any other turn"]),
               ("extract · usage", ["Learned memories", "Usage rows, which tell no one"])]
    f.arrow("M615,394 V432")
    f.text(631, 420, "requests", "s")
    for i, (h, lines) in enumerate(writers):
        x = 72 + i * 370
        f.card(x, 436, 346, 116)
        f.text(x + 22, 474, h, "hs code")
        for j, line in enumerate(lines):
            f.text(x + 22, 506 + j * 26, line, "b")
        f.arrow(f"M{x + 173},554 V588")
    y = 592
    f.card(72, y, 1456, 190, TINT, INDIGO, r=18)
    f.text(100, y + 40, "store.Update", "h code")
    f.text(100, y + 66, "Every change, one transaction", "s")
    for k in range(6):
        f.block(100 + k * 40, y + 86, 32, 34, [LINE, INDIGO_2, INDIGO_2, "#7d74b0", "#5a5290", INDIGO][k])
    f.text(100, y + 144, "write queue", "s")
    f.arrow(f"M346,{y + 103} H388")
    f.card(392, y + 84, 150, 38, "#ffffff", INDIGO, r=10)
    f.text(467, y + 109, "one writer", "chip", "middle", f"fill:{INDIGO}")
    f.arrow(f"M544,{y + 103} H586")
    f.add(f'<g transform="translate(596,{y + 80})" fill="none" stroke="{INDIGO}" stroke-width="3"><ellipse cx="22" cy="8" rx="22" ry="8"/>'
          f'<path d="M0,8 V40 A22,8 0 0 0 44,40 V8"/><path d="M0,24 A22,8 0 0 0 44,24"/></g>')
    f.text(656, y + 100, "SQLite", "hs")
    f.text(656, y + 124, "WAL, one write connection", "s")
    f.add(f'<line x1="900" y1="{y + 24}" x2="900" y2="{y + 136}" stroke="{LINE}" stroke-width="2"/>')
    f.text(930, y + 40, "Store.Read", "h code")
    f.text(930, y + 72, "Four read-only connections, each on a snapshot", "b")
    f.text(930, y + 98, "of the last commit; a read never waits for a write", "b")
    f.text(930, y + 124, "and the gateway answers reads side by side", "s")
    f.text(100, y + 172, "append-only log → threads · turns · items  ·  tasks  ·  workspaces · environments  ·  model catalog · users · skills  ·  memories · cron jobs · todos", "s")
    f.arrow(f"M800,{y + 192} V{y + 226}")
    f.text(816, y + 214, "after each commit", "s")
    y = 822
    f.card(72, y, 1456, 76, TINT_2, INDIGO_2, r=38)
    f.text(104, y + 46, "store.Feed", "h code")
    for k in range(5):
        f.block(262 + k * 30, y + 22, 24, 32, [INDIGO_2, "#7d74b0", "#5a5290", INDIGO, INDIGO][k], r=6)
    f.arrow(f"M416,{y + 38} H446")
    f.text(458, y + 45, "One event queue, one dispatcher: told in seq order, deltas in between · it only wakes; the work waits in SQLite", "b")
    subs = [("gateway", ["Notifications to the", "user's connections,", "approval requests"]),
            ("schedule.Threads", ["Runnable thread:", "a Runner in a free", "slot, top level first"]),
            ("schedule.Tasks", ["Background call:", "runs in a task slot,", "4 per user"]),
            ("extract", ["Turn ended: learn", "memories with the", "user's learn model"]),
            ("cron", ["Job changed: send", "what is due, rearm", "the timer"]),
            ("mcp.Users", ["Server changed:", "close it, reconnect", "on next use"]),
            ("host", ["Catalog changed:", "new clients for the", "models that changed"])]
    for i, (h, lines) in enumerate(subs):
        x = 72 + i * 210
        f.arrow(f"M{x + 98},{y + 78} V{y + 112}")
        f.card(x, y + 116, 196, 128)
        f.text(x + 18, y + 150, h, "hs code", style="font-size:16px")
        for j, line in enumerate(lines):
            f.text(x + 18, y + 178 + j * 24, line, "s")
    f.arrow("M380,1068 V1102")
    f.text(396, 1092, "starts", "s")
    y = 1106
    f.card(72, y, 440, 128, "#ffffff", INDIGO, r=16)
    f.text(94, y + 38, "run.Runner", "hs code")
    for j, line in enumerate(["Each step reads the view, asks agent.Next,", "calls the model or tools, appends the result;", "waiting on a person frees the slot"]):
        f.text(94, y + 70 + j * 24, line, "s")
    f.arrow(f"M514,{y + 64} H532")
    f.card(536, y, 440, 128)
    f.text(558, y + 38, "Models", "hs")
    for j, line in enumerate(["The deployment's catalog, kept in the store", "and set by its operator; each user chooses;", "4 calls at a time per model by default"]):
        f.text(558, y + 70 + j * 24, line, "s")
    f.card(1000, y, 528, 128)
    f.text(1022, y + 38, "Tools", "hs")
    for j, line in enumerate(["Built-in · MCP servers · the embedder's own", "Permission decided as each call is written", "Run in the sandbox of the conversation's environment"]):
        f.text(1022, y + 70 + j * 24, line, "s")
    f.arrow(f"M292,{y + 130} V{y + 160} H1264 V{y + 132}")
    f.text(780, y + 154, "invokes, read-only calls side by side", "s", "middle")
    f.arrow(f"M72,{y + 64} H44 V494 H68", dash=True)
    f.add(f'<text class="s" transform="translate(36,{(y + 494) // 2 + 40}) rotate(-90)" text-anchor="middle">writes back through store.Update</text>')
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


CHART_H = 1000
BARS_H = 800


def hbars(f, x, y, w, title, unit, rows, top):
    """A small panel of horizontal bars: (name, value, label, color), the longest bar standing for top."""
    f.text(x, y, title, "hs")
    f.text(x, y + 28, unit, "tick")
    bx, bw = x + 230, w - 230 - 130
    for i, (name, v, label, color) in enumerate(rows):
        yy = y + 58 + i * 62
        width = max(bw * v / top, 6)
        f.text(x, yy + 27, name, "lab")
        f.block(bx, yy, width, 40, color, r=6)
        f.text(bx + width + 14, yy + 27, label, "val")


COMPARE = {
    "names": ["Ollupi", "pi (SDK)", "pi-durable", "DeepSeek Harness"],
    "parked_kb": [14, 90, 64, 147],
    "memory_2048": [398, 777, 925, 1371],
    "slower_2048": [76, 28, 58, 330],
    "burst_done": [3000, 483, 527, 1764],
    "crash": [("Every turn completes", True), ("Turns in progress stop", False),
              ("Every turn completes", True), ("980 of 1000 interrupted", False)],
    "background": [("200 of 200 report back", True), ("No background calls", False),
                   ("No background calls", False), ("0 of 200 report back", False)],
}


def compare():
    c = COMPARE
    f = Figure(1320, "OLLUPI BENCH · COMPARISON", "Same load, four harnesses: what each one costs and what survives",
               "Same simulated provider and driver, one run each. Memory is phys_footprint of the process that hosts the sessions",
               "pi-coding-agent 1.5.2 · pi-durable 1.5.2 · DeepSeek Harness 0.2.2-rc.5 · " + ENV, "bench/compare")

    def rows(values, fmt):
        return [(n, v, fmt(v), INDIGO if i == 0 else BASE) for i, (n, v) in enumerate(zip(c["names"], values))]

    def outcomes(x, y, title, sub, labels):
        f.text(x, y, title, "hs")
        f.text(x, y + 28, sub, "tick")
        for i, (name, (label, kept)) in enumerate(zip(c["names"], labels)):
            yy = y + 58 + i * 62
            f.text(x, yy + 27, name, "lab")
            f.block(x + 230, yy, 460, 40, TINT_2 if kept else GRID, label, INDIGO_DEEP if kept else INK2, r=6)

    hbars(f, PAD, 196, 690, "Memory per parked session", "KB, lower is better", rows(c["parked_kb"], lambda v: f"{v:g}"), max(c["parked_kb"]))
    hbars(f, 838, 196, 690, "Memory with 2048 sessions running", "MB at peak, lower is better",
          rows(c["memory_2048"], lambda v: f"{v:,}"), max(c["memory_2048"]))
    hbars(f, PAD, 540, 690, "Turn time with 2048 sessions running", "p50 over running alone, lower is better",
          rows(c["slower_2048"], lambda v: f"+{v:g}%"), max(c["slower_2048"]))
    hbars(f, 838, 540, 690, "3000 turns submitted at once, quota 256", "Turns completed; the rest failed after their retries",
          rows(c["burst_done"], lambda v: f"{v:,}"), 3000)
    outcomes(PAD, 884, "After kill -9 mid-run", "Turns in progress, repeated kills", c["crash"])
    outcomes(838, 884, "Background commands, kill -9 while they run", "Their results, back in the conversation", c["background"])
    f.save("bench-compare.svg")


def capacity():
    pts = [(512, 17.4, 21.6, 0.23), (1024, 18.4, 24.6, 1.39), (1536, 17.8, 22.4, 0.78), (2048, 21.5, 27.1, 4.21),
           (3072, 32.0, 43.8, 14.25), (4096, 48.2, 69.8, 29.83)]
    alone = 17.0
    f = Figure(CHART_H, "OLLUPI BENCH · CAPACITY", "Up to 1536 sessions a turn runs as fast as alone; past that, it waits for SQLite",
               "Closed loop: each session starts a turn as soon as the last ends. A turn is 3 model calls (5 s ± 2 s) and 2 tool calls (1 s), 17 s alone",
               ENV, "go run ./bench steady -c N")
    f.kpis([("+5%", "", "turn p50 over alone at 1536 sessions"), ("241", "calls/s", "highest model call throughput (3072 sessions)"),
            ("126 / 44", "", "SQLite reads / writes per turn")])
    xt = [(v, f"{v:,}") for v in (1024, 2048, 3072, 4096)]
    left = Plot(f, 160, 470, 600, 360, (0, 4400), (0, 80))
    left.frame("Turn duration", "seconds", [(v, f"{v}") for v in (0, 20, 40, 60, 80)], xt, "Sessions running at once")
    left.line([(0, alone), (4400, alone)], FAINT, width=2, dashed=True)
    left.label(4400, alone, "Alone: 17 s", "cap", "end", dy=30)
    left.line([(x[0], x[2]) for x in pts], INDIGO_2)
    left.dots([(x[0], x[2]) for x in pts], INDIGO_2)
    left.line([(x[0], x[1]) for x in pts], INDIGO)
    left.dots([(x[0], x[1]) for x in pts], INDIGO)
    left.label(4096, 69.8, "p99", "val", "start", dx=16, dy=6)
    left.label(4096, 48.2, "p50", "val", "start", dx=16, dy=6)
    right = Plot(f, 940, 470, 600, 360, (0, 4400), (0, 35))
    right.frame("Where the extra time goes", "seconds per turn", [(v, f"{v}") for v in (0, 10, 20, 30)], xt, "Sessions running at once")
    right.line([(x[0], x[1] - alone) for x in pts], INDIGO)
    right.dots([(x[0], x[1] - alone) for x in pts], INDIGO)
    right.line([(x[0], x[3]) for x in pts], ROSE, dashed=True)
    right.dots([(x[0], x[3]) for x in pts], ROSE)
    right.label(4096, 31.2, "31.2 s", "val", "end", dx=-18, dy=-10)
    f.legend(W - PAD, 372, [(INDIGO, "p50 over alone", False), (ROSE, "Waiting for a SQLite connection", True)])
    f.save("bench-capacity.svg")


def background():
    f = Figure(BARS_H, "OLLUPI BENCH · BACKGROUND CALLS", "A long command in the background leaves its slot to other turns",
               "256 turns each run a 30 s command on 32 slots, 32 commands at a time, while a new session sends a 5 s turn every second",
               ENV, "go run ./bench background -mode background | foreground")
    f.kpis([("10", "ms", "new session's wait, p50, commands in the background"), ("144", "s", "the same, commands in their turns"),
            ("243 / 257", "s", "all 256 commands done; 32 at a time allow 240 s")])
    hbars(f, PAD, 380, 690, "Wait for the first model request", "New sessions, p50, seconds",
          [("In the background", 0.01, "0.01", INDIGO), ("In the turn", 144.1, "144.1", ROSE)], 144.1)
    hbars(f, 838, 380, 690, "New session's turn", "p50, seconds; 5.0 s alone",
          [("In the background", 5.1, "5.1", INDIGO), ("In the turn", 149.0, "149.0", ROSE)], 149.0)
    f.text(PAD, 600, "In the turn, a command holds its slot for 30 s, so new turns queue behind the long ones.", "b")
    f.text(PAD, 630, "In the background, the turn launches the command, answers, and gives the slot back until the result arrives.", "b")
    f.save("bench-background.svg")


def recovery():
    f = Figure(CHART_H, "OLLUPI BENCH · RECOVERY", "After kill -9 every turn finishes and no tool call runs twice",
               "Crash: 500 sessions under mixed load (tool calls, questions, subagents, background calls), killed 30 times. Wake-up: an idle session's next model request",
               ENV, "go run ./bench crash | crash -lease | resume")
    f.kpis([("600", "/ 600", "turns completed after 30 kills"), ("0", "", "tool calls run twice"),
            ("29", "ms", "restart, p50"), ("1.3", "s", "standby takes over, p50 (1.5 s lease)")])
    data = [("10 turns", 3.7, 4.1), ("100 turns", 8.6, 9.4), ("1000 turns", 88.8, 91.6)]
    p = Plot(f, 160, 470, 1368, 360, (0, 3), (0, 100))
    p.frame("Waking an idle session", "ms until its model request goes out", [(v, f"{v}") for v in (0, 25, 50, 75, 100)],
            [(i + 0.5, d[0]) for i, d in enumerate(data)], "Session history")
    for i, (_, p50, p95) in enumerate(data):
        cx = p.px(i + 0.5)
        p.bar(cx - 52, p50, 92, INDIGO)
        p.bar(cx + 52, p95, 92, INDIGO_2)
        f.text(cx - 52, p.py(p50) - 14, f"{p50:g}", "val", "middle")
        f.text(cx + 52, p.py(p95) - 14, f"{p95:g}", "val", "middle")
    f.legend(W - PAD, 422, [(INDIGO, "p50", False), (INDIGO_2, "p95", False)], bars=True)
    f.save("bench-recovery.svg")


def users():
    f = Figure(BARS_H, "OLLUPI BENCH · USERS", "With a per-user slot limit, one heavy user no longer holds up the rest",
               "One shard, 512 slots: 32 users keep 8 sessions busy each, beside one user who keeps 1024 busy. Each user has a key of their own",
               ENV, "go run ./bench users | users -user-slots 64")
    f.kpis([("8", "ms", "others' wait p50, at most 64 slots per user"), ("17.1", "s", "others' turn p50; 17 s alone"),
            ("0", "", "429s for others when the heavy user's key runs out")])
    hbars(f, PAD, 380, 690, "Others' wait for the first model request", "p50, seconds",
          [("No limit", 205.7, "205.7", ROSE), ("64 slots per user", 0.008, "0.008", INDIGO)], 205.7)
    hbars(f, 838, 380, 690, "Others' turn", "p50, seconds",
          [("No limit", 223.7, "223.7", ROSE), ("64 slots per user", 17.1, "17.1", INDIGO)], 223.7)
    f.text(PAD, 600, "Without a limit, the heavy user's 1024 sessions take every slot and the others queue behind them.", "b")
    f.text(PAD, 630, "When the heavy user's key is over its provider quota, its 7,389 429s and 148 failed turns stay its own.", "b")
    f.save("bench-users.svg")


def soak():
    rows = list(csv.DictReader(open(SOAK_CSV)))
    turns = sum(int(r["turns_ended"]) for r in rows)
    minutes = [float(r["minutes"]) for r in rows]
    alone = 3.4
    f = Figure(CHART_H, "OLLUPI BENCH · LONG RUN", f"8 minutes, {turns:,} turns: turn time holds, memory stays between 100 and 124 MB",
               "256 sessions send turns back to back; after 10 turns a session is replaced by a new one, so each turn's work stays the same. Model calls 1 s, tool calls 0.2 s",
               ENV, "go run ./bench soak -c 256 -d 8m -history 10 -latency 1s -jitter 200ms -tool 200ms")
    goroutines = [int(r["goroutines"]) for r in rows]
    f.kpis([(f"{min(goroutines)} – {max(goroutines)}", "", "goroutines"), ("4.1", "MB", "WAL, throughout"),
            (f"{max(float(r['gc_pause_p99_ms']) for r in rows):.2f}", "ms", "highest GC pause p99")])
    xt = [(v, f"{v}") for v in (0, 2, 4, 6, 8)]
    left = Plot(f, 160, 470, 600, 360, (0, 8.5), (0, 8))
    left.frame("Turn duration", "seconds", [(v, f"{v}") for v in (0, 2, 4, 6, 8)], xt, "Minutes")
    left.line([(0, alone), (8.5, alone)], FAINT, width=2, dashed=True)
    left.label(8.5, alone, "Alone: 3.4 s", "cap", "end", dy=30)
    left.line(list(zip(minutes, (float(r["p99_s"]) for r in rows))), INDIGO_2)
    left.line(list(zip(minutes, (float(r["p50_s"]) for r in rows))), INDIGO)
    left.label(minutes[-1], float(rows[-1]["p99_s"]), "p99", "val", "start", dx=12, dy=6)
    left.label(minutes[-1], float(rows[-1]["p50_s"]), "p50", "val", "start", dx=12, dy=-6)
    right = Plot(f, 940, 470, 600, 360, (0, 8.5), (0, 200))
    right.frame("Resident memory", "MB", [(v, f"{v}") for v in (0, 50, 100, 150, 200)], xt, "Minutes")
    rss = list(zip(minutes, (float(r["rss_mb"]) for r in rows)))
    right.line(rss, INDIGO)
    right.label(*rss[0], f"{rss[0][1]:g} MB", "val", "start", dy=-16)
    right.label(*rss[-1], f"{rss[-1][1]:g} MB", "val", "end", dy=-16)
    f.save("bench-soak.svg")


architecture()
comparison()
capacity()
background()
recovery()
users()
soak()
compare()
