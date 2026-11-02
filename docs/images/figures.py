"""Ollupi figures: every README image in one visual system.

Each figure is 1600 wide on white. A header carries the pixel wolf, an eyebrow, a title that states the point and a
line of context; a footer carries the conditions or the source. Indigo is Ollupi (the wolf's fur and outline), rose is
the per-agent isolation it replaces (the wolf's nose), pink is tool execution (its ears), slate is neutral; waiting is
hatched. Text never takes a series color.

Run from anywhere: python3 docs/images/figures.py. The charts' numbers are the bench's output, written in below; the
burst timeline is read from bench-burst.csv, which bench burst -timeline writes. After a new measurement, update both
and run this again.
"""
import csv
import math
import os
import re

OUT = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(os.path.dirname(OUT))
BURST_CSV = f"{OUT}/bench-burst.csv"
W, PAD = 1600, 72

INDIGO, INDIGO_2, INDIGO_DEEP = "#4f46e5", "#818cf8", "#1e1b4b"
TINT, TINT_2, LINE = "#eef2ff", "#e0e7ff", "#c7d2fe"
ROSE, ROSE_TINT, ROSE_LINE = "#f43f5e", "#fff1f2", "#fecdd3"
PINK = "#ec4899"
INK2, MUTED, FAINT = "#334155", "#64748b", "#94a3b8"
GRID, RULE, BASE, PAPER = "#eef2f6", "#e2e8f0", "#cbd5e1", "#f8fafc"
FONT = '-apple-system, "PingFang SC", "Hiragino Sans GB", "Microsoft YaHei", "Noto Sans SC", sans-serif'
MONO = "ui-monospace, 'SF Mono', Menlo, monospace"
ENV = "Apple M1 · 8 核 · 8 GB · 单个 SQLite 库 · 模型与工具为模拟"
SOURCE = "github.com/0xfakeSpike/ollupi"

WOLF = re.search(r"<svg[^>]*>(.*)</svg>", open(f"{ROOT}/docs/logo/mark.svg").read(), re.S).group(1)


def esc(s):
    return s.replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;")


def tw(s, size):
    """Approximate rendered width: CJK at the font size, Latin at a little over half of it."""
    return sum(size if ord(c) > 0x2e80 else size * 0.56 for c in s)


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
            f'<svg x="{PAD}" y="40" width="68" height="68" viewBox="0 0 240 240">{WOLF}</svg>',
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
            cx -= tw(label, 17)
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
    f = Figure(960, "OLLUPI · 架构", "一个进程承载所有会话",
                "会话、子代理与定时任务在同一个 agent 内调度，状态全部写入 SQLite", "每一步先写入数据库，再执行", SOURCE)
    # clients and gateway
    f.card(560, 196, 480, 60, TINT, LINE)
    f.text(800, 233, "ollupi-desktop · 其他客户端", "hs", "middle")
    f.arrow("M800,258 V300")
    f.arrow("M800,300 V258")
    f.text(816, 284, "JSON-RPC 2.0 · WebSocket", "s")
    f.card(360, 302, 880, 64)
    f.text(392, 342, "gateway", "h code")
    f.text(510, 341, "请求分发 · 事件推送 · 协议契约与方法表同源生成", "b")
    f.arrow("M800,368 V404")
    # agent
    f.card(330, 406, 940, 236, TINT, INDIGO, r=18)
    f.text(360, 444, "agent", "h code")
    mods = [("调度器", ["共享执行池，默认 32 个槽位", "等待时释放槽位", "普通会话先于子代理"]),
            ("runtime", ["调用模型前自库中重读", "模型调用 → 工具执行", "步骤先写库再执行"]),
            ("权限 · 脱敏", ["三层权限规则", "Seatbelt 沙箱", "发往模型前替换敏感值"])]
    for i, (h, lines) in enumerate(mods):
        x = 360 + i * 300
        f.card(x, 466, 280, 156)
        f.text(x + 22, 504, h, "hs code" if h == "runtime" else "hs")
        for j, line in enumerate(lines):
            f.text(x + 22, 540 + j * 26, line, "b")
    # side services
    for i, (h, lines) in enumerate([("记忆", ["从已结束的 turn 提炼", "全局 · general · 工作区"]),
                                     ("定时任务", ["到点向会话发送消息", "与其他 turn 同池排队"])]):
        y = 420 + i * 116
        f.card(72, y, 228, 100)
        f.text(94, y + 36, h, "hs")
        for j, line in enumerate(lines):
            f.text(94, y + 64 + j * 24, line, "s")
        f.arrow(f"M300,{y + 50} H326")
    f.card(1300, 420, 228, 100)
    f.text(1322, 456, "模型连接", "hs")
    f.text(1322, 484, "OpenAI · Anthropic", "s")
    f.text(1322, 508, "Gemini · 按连接限制并发", "s")
    f.arrow("M1272,470 H1296")
    f.card(1300, 552, 228, 90, PAPER, BASE, dash=True)
    f.text(1322, 590, "模型服务商", "hs")
    f.text(1322, 618, "只接收占位符", "s")
    f.arrow("M1414,522 V548")
    # tools
    f.arrow("M800,644 V680")
    f.text(816, 668, "经权限检查后调用", "s")
    tools = [("内置工具", "子代理 · 计划 · 提问"), ("run_command", "在沙箱内执行"), ("MCP 服务器", "stdio · HTTP"), ("应用工具", "tools.Provider")]
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
    f.text(166, 866, "thread · turn · item · 记忆 · 定时任务 · 权限规则 · 消息全文索引", "b")
    f.text(1500, 836, "先写入，再执行", "hs", "end")
    f.text(1500, 866, "重启后队列与进行中的 turn 均可恢复", "b", "end")
    f.arrow("M186,638 V792", dash=True)
    f.arrow("M800,766 V792")
    f.save("architecture.svg")


def comparison():
    f = Figure(940, "OLLUPI · 对比", "同样的机器，承载更多会话",
                "逐个隔离时，等待中的 agent 仍占用一个进程；Ollupi 在等待时释放槽位，由其他 turn 接续", "字母为会话，斜纹为等待", SOURCE)
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
            (0, 72, "逐个隔离", "每个 agent 一个虚拟机或容器", ROSE, ROSE_TINT, ROSE_LINE),
            (1, 816, "Ollupi", "一个进程，一个共享执行池", INDIGO, TINT, LINE)]:
        f.card(x0, 196, 712, 600, tint, line, r=18)
        f.text(x0 + 32, 242, title, "h")
        f.text(x0 + 32, 272, sub, "s")
    lx = 72 + 164
    lane(lx, 300, "进程 1 · 会话 A", [("run", .28, "A"), ("wait", .44, "等审批"), ("run", .28, "A")], ROSE)
    lane(lx, 364, "进程 2 · 会话 B", [("run", .2, "B"), ("wait", .3, "等回答"), ("run", .22, "B"), ("wait", .28, "等审批")], ROSE)
    lane(lx, 428, "进程 3 · 会话 C", [("run", .36, "C"), ("wait", .38, "等回答"), ("run", .26, "C")], ROSE)
    f.text(lx - 20, 522, "会话 D、E", "lab", "end")
    f.card(lx, 496, lane_w - 6, 44, "#ffffff", ROSE_LINE, r=8, dash=True)
    f.text(lx + (lane_w - 6) / 2, 524, "需要新的进程", "s", "middle")
    rx = 816 + 164
    lane(rx, 300, "槽位 1", [("run", .2, "A"), ("run", .22, "D"), ("run", .2, "定时"), ("run", .2, "A"), ("run", .18, "E")], INDIGO)
    lane(rx, 364, "槽位 2", [("run", .2, "B"), ("run", .38, "E"), ("run", .2, "B"), ("run", .22, "D")], INDIGO)
    lane(rx, 428, "槽位 3", [("run", .3, "C"), ("run", .44, "D 的子代理"), ("run", .26, "C")], INDIGO)
    f.text(rx - 20, 516, "等待中", "lab", "end")
    f.text(rx - 20, 538, "不占槽位", "s", "end")
    for i, (x, y, t) in enumerate([(0, 496, "A 等审批"), (.34, 496, "B 等回答"), (.68, 496, "B 等审批"), (.17, 534, "C 等回答")]):
        f.add(f'<rect x="{rx + x * lane_w:.1f}" y="{y}" width="150" height="30" rx="8" fill="url(#hatch)" stroke="{BASE}" stroke-width="1.5"/>')
        f.text(rx + x * lane_w + 75, y + 21, t, "s", "middle")
    for side, x0, items in [(0, 72, ["一个进程只承载一个会话", "等待时进程挂起，仍占用内存", "调度状态在内存，进程退出即丢失", "定时任务由外部 cron 唤起进程"]),
                            (1, 816, ["一个进程运行所有会话、子代理与定时任务", "等待时释放槽位，其他 turn 接续", "调度状态在 SQLite，重启后从中断处继续", "定时任务到点向会话发送消息，同池排队"])]:
        f.add(f'<line x1="{x0 + 32}" y1="608" x2="{x0 + 680}" y2="608" stroke="{ROSE_LINE if side == 0 else LINE}" stroke-width="2"/>')
        for i, t in enumerate(items):
            f.add(f'<rect x="{x0 + 34}" y="{640 + i * 36 - 7}" width="8" height="8" fill="{ROSE if side == 0 else INDIGO}"/>')
            f.text(x0 + 56, 640 + i * 36, t, "b")
    f.save("cli-vs-ollupi.svg")


def pool():
    f = Figure(940, "OLLUPI · 执行池", "所有 turn 共用一个执行池",
                "等待时释放槽位，模型并发只分配给正在调用模型的 turn", "agent 的耗时主要在等待模型；执行池让同时进行的模型调用尽可能多", SOURCE)
    for i, (h, s) in enumerate([("用户消息", "任意会话"), ("子代理", "普通的会话与 turn"), ("定时任务", "到点向会话发送消息")]):
        y = 236 + i * 118
        f.card(72, y, 228, 90)
        f.text(96, y + 38, h, "hs")
        f.text(96, y + 66, s, "s")
        f.arrow(f"M300,{y + 45} C340,{y + 45} 340,{392} 372,{392}")
    # queue
    f.card(376, 212, 300, 380, TINT, LINE, r=18)
    f.text(400, 252, "队列", "h")
    f.text(400, 280, "数据库中可推进的 turn", "s")
    for i, t in enumerate(["会话 A · 第 3 轮", "会话 B · 第 1 轮", "子代理 A-1", "子代理 A-2", "定时任务 · 每日汇总"]):
        f.card(400, 300 + i * 54, 252, 42, "#ffffff", LINE, r=10)
        f.text(418, 327 + i * 54, t, "b")
    f.text(376, 618, "槽位已满时，普通会话先于子代理，", "s")
    f.text(376, 642, "同类按就绪先后执行", "s")
    f.arrow("M678,402 H716")
    # slots
    f.card(720, 212, 420, 380, "#ffffff", INDIGO, r=18)
    f.text(744, 252, "槽位", "h")
    f.text(744, 280, "默认 32 个；一个 turn 占一个，不等人", "s")
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
    for i, (c, t) in enumerate([(INDIGO, "调用模型"), (PINK, "执行工具"), (None, "空闲")]):
        x = 744 + i * 128
        if c:
            f.add(f'<rect x="{x}" y="{514}" width="16" height="16" rx="4" fill="{c}"/>')
        else:
            f.add(f'<rect x="{x}" y="{514}" width="16" height="16" rx="4" fill="{PAPER}" stroke="{RULE}" stroke-width="2"/>')
        f.text(x + 24, 528, t, "s")
    f.text(744, 566, "工具执行期间占用槽位，不占用模型并发", "s")
    f.arrow("M1142,402 H1180")
    # model concurrency
    f.card(1184, 212, 344, 380, "#ffffff", LINE, r=18)
    f.text(1208, 252, "模型并发", "h")
    f.text(1208, 280, "按连接计，默认每个连接 4 个", "s")
    for j, conn in enumerate(["连接 A", "连接 B"]):
        y = 318 + j * 120
        f.text(1208, y, conn, "lab")
        for k in range(4):
            f.add(f'<rect x="{1208 + k * 76}" y="{y + 16}" width="66" height="40" rx="8" fill="{INDIGO}"/>')
    f.text(1208, 566, "满额时调用排队，不越过账户额度", "s")
    # waiting
    f.arrow("M930,594 V676")
    f.text(946, 640, "需要审批、提问或子代理", "s")
    f.add(f'<rect x="720" y="680" width="420" height="156" rx="18" fill="url(#hatch)" stroke="{BASE}" stroke-width="2"/>')
    f.add(f'<rect x="740" y="696" width="380" height="124" rx="12" fill="#ffffff"/>')
    f.text(762, 732, "等待中", "h")
    f.text(762, 760, "释放槽位，不占用 goroutine 与模型并发", "s")
    for i, t in enumerate(["等审批", "等回答", "等子代理"]):
        f.add(f'<rect x="{762 + i * 116}" y="778" width="104" height="28" rx="14" fill="{TINT}"/>')
        f.text(762 + i * 116 + 52, 797, t, "chip", "middle", f"fill:{INDIGO}")
    f.arrow("M718,758 H652 V596")
    f.text(636, 700, "回答、审批或子代理结果", "s", "end")
    f.text(636, 722, "到达后重新就绪", "s", "end")
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
    f = Figure(CHART_H, "OLLUPI BENCH · 内存", "会话挂起时只占数据库，不占进程内存",
               "进程常驻内存（RSS）随会话数的变化，双对数坐标。对照组为每个会话一个 Ollupi 进程，未计容器开销",
               ENV, "go run ./bench parked | steady | isolated")
    f.kpis([("< 0.3", "KB", "每个挂起会话的内存（5 万个挂起会话实测）"),
            ("≈ 160", "KB", "每个活跃会话的内存，含其 HTTP 连接"),
            ("20.0", "MB", "对照组：每个会话一个进程")])
    p = Plot(f, 190, 470, 1250, 360, (100, 100_000), (10, 2 * 1024 * 1024), xlog=True, ylog=True)
    p.frame("进程内存", "RSS，对数坐标", [(v, size(v)) for v in (10, 100, 1024, 10 * 1024, 100 * 1024, 1024 * 1024)],
            [(v, f"{v:,}") for v in (100, 1000, 10_000, 100_000)], "会话数，对数坐标")
    p.line([(100, 2000), (100_000, 2_000_000)], ROSE)
    p.label(30_000, 600_000, "每会话一个进程", "lab", "end", dx=-14, dy=-14)
    active = [(256, 82), (1024, 214), (1536, 293), (2048, 365), (4096, 589)]
    p.line(active, INDIGO)
    p.dots(active, INDIGO)
    p.label(4096, 589, "Ollupi · 活跃会话", "lab", "start", dx=18, dy=6)
    waiting = [(1000, 28.4), (10_000, 38.6), (50_000, 36.8)]
    p.line(waiting, INDIGO_2)
    p.dots(waiting, INDIGO_2)
    p.label(50_000, 36.8, "Ollupi · 挂起会话", "lab", "end", dy=-20)
    f.legend(W - PAD, 422, [(INDIGO, "Ollupi · 活跃会话", False), (INDIGO_2, "Ollupi · 挂起会话", False), (ROSE, "每会话一个进程", False)])
    f.save("bench-memory.svg")


def capacity():
    pts = [(256, 17.1, 20.4, 39), (1024, 17.3, 20.7, 141), (1536, 17.8, 21.3, 212), (2048, 22.1, 34.8, 220), (4096, 29.5, 40.9, 350)]
    f = Figure(CHART_H, "OLLUPI BENCH · 容量", "约 1500 个活跃会话以内，turn 耗时与单独运行基本持平",
               "闭环负载：每个会话在上一个 turn 结束后立即发起下一个。每个 turn 3 次模型调用（5 s ± 2 s）、2 次工具调用（1 s），单独运行约 17 s",
               ENV, "go run ./bench steady -c 1536")
    f.kpis([("+5%", "", "1536 个活跃会话时 turn 耗时 p50 的增幅"), ("350", "次/秒", "4096 个活跃会话时的模型调用吞吐"), ("11.6", "KB", "每个 turn 的磁盘占用")])
    left = Plot(f, 160, 470, 600, 360, (0, 4400), (0, 45))
    left.frame("turn 耗时", "秒", [(v, f"{v}") for v in (0, 15, 30, 45)], [(v, f"{v:,}") for v in (1024, 2048, 3072, 4096)], "同时活跃的会话")
    left.line([(0, 17), (4400, 17)], FAINT, width=2, dashed=True)
    left.label(4400, 17, "单独运行 17 s", "cap", "end", dy=30)
    left.line([(x[0], x[2]) for x in pts], INDIGO_2)
    left.dots([(x[0], x[2]) for x in pts], INDIGO_2)
    left.line([(x[0], x[1]) for x in pts], INDIGO)
    left.dots([(x[0], x[1]) for x in pts], INDIGO)
    left.label(4096, 40.9, "p95", "val", "start", dx=16, dy=6)
    left.label(4096, 29.5, "p50", "val", "start", dx=16, dy=6)
    f.legend(760, 422, [(INDIGO, "p50", False), (INDIGO_2, "p95", False)])
    right = Plot(f, 940, 470, 600, 360, (0, 4400), (0, 400))
    right.frame("模型调用吞吐", "次/秒", [(v, f"{v}") for v in (0, 100, 200, 300, 400)], [(v, f"{v:,}") for v in (1024, 2048, 3072, 4096)], "同时活跃的会话")
    right.line([(x[0], x[3]) for x in pts], INDIGO)
    right.dots([(x[0], x[3]) for x in pts], INDIGO)
    for x in pts:
        if x[0] in (1024, 2048, 4096):
            right.label(x[0], x[3], f"{x[3]}", "val", "middle", dy=-20)
    f.save("bench-capacity.svg")


def burst():
    rows = [(float(r["seconds"]), int(r["in_flight"]), int(r["turns_ended"])) for r in csv.DictReader(open(BURST_CSV))]
    end = max(t for t, _, _ in rows)
    f = Figure(CHART_H, "OLLUPI BENCH · 突发与额度", "突发负载按额度排队，在途请求不超额度，无 429",
               "一次提交 3000 个 turn；模型连接与服务商均限 256 并发。每个 turn 3 次模型调用（5 s ± 2 s）、2 次工具调用（1 s）",
               ENV, "go run ./bench burst -n 3000 -slots 512 -calls 256 -latency 5s")
    f.kpis([("256", "/ 256", "在途请求峰值 / 额度"), ("0", "次", "服务商返回 429"), ("181.5", "s", "全部完成耗时，理论下限 175.8 s")])
    xt = [(v, f"{v}") for v in range(0, int(end) + 1, 30)]
    left = Plot(f, 160, 470, 600, 360, (0, end), (0, 300))
    left.frame("在途模型请求", "个", [(v, f"{v}") for v in (0, 100, 200, 300)], xt, "秒")
    left.line([(0, 256), (end, 256)], FAINT, width=2, dashed=True)
    left.label(end, 256, "额度 256", "cap", "end", dy=-12)
    left.line([(0, 0)] + [(t, x) for t, x, _ in rows], INDIGO)
    right = Plot(f, 940, 470, 600, 360, (0, end), (0, 3000))
    right.frame("已完成 turn", "个", [(v, f"{v:,}") for v in (0, 1000, 2000, 3000)], xt, "秒")
    right.line([(0, 0), (175.8, 3000)], FAINT, width=2, dashed=True)
    right.line([(0, 0)] + [(t, e) for t, _, e in rows], INDIGO)
    f.legend(W - PAD, 422, [(INDIGO, "实测", False), (FAINT, "理论下限", True)])
    f.save("bench-burst.svg")


def recovery():
    f = Figure(CHART_H, "OLLUPI BENCH · 恢复", "进程重启与会话唤醒均在毫秒级完成",
               "唤醒：向闲置于数据库的会话发送消息，至其模型请求发出的耗时。崩溃：混合负载下随机 kill -9 共 90 次",
               ENV, "go run ./bench resume | crash")
    f.kpis([("0", "/ 960", "kill -9 后丢失的 turn"), ("0", "次", "重复执行的工具调用"), ("448", "ms", "重启耗时：1 万挂起会话 + 500 个运行中 turn")])
    data = [("10 轮", 5.2, 6.6), ("100 轮", 18.8, 22.5), ("1000 轮", 73.2, 150.4)]
    p = Plot(f, 160, 470, 1368, 360, (0, 3), (0, 160))
    p.frame("会话唤醒耗时", "毫秒", [(v, f"{v}") for v in (0, 40, 80, 120, 160)], [(i + 0.5, d[0]) for i, d in enumerate(data)], "会话历史长度")
    for i, (_, p50, p95) in enumerate(data):
        cx = p.px(i + 0.5)
        p.bar(cx - 52, p50, 92, INDIGO)
        p.bar(cx + 52, p95, 92, INDIGO_2)
        f.text(cx - 52, p.py(p50) - 14, f"{p50:g}", "val", "middle")
        f.text(cx + 52, p.py(p95) - 14, f"{p95:g}", "val", "middle")
    f.legend(W - PAD, 422, [(INDIGO, "p50", False), (INDIGO_2, "p95", False)], bars=True)
    f.save("bench-recovery.svg")


architecture()
comparison()
pool()
memory()
capacity()
burst()
recovery()
