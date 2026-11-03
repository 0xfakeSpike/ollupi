// Experiment 4, crash (pi-durable): a child process runs n turns over one SQLite file; the parent kill -9s it at random
// moments, starts it again on the same file (Harness.open + resume()), and at the end checks that every submission
// settled, which bench__work calls ran twice (each call writes its call id to tools.log before working), how many calls
// a crash cut off, and how long a restart took. Model 200 ms, tool 300 ms, as ollupi's `bench crash`.
//
//   node crash.mjs --n 50 --turns 20 --kills 30   (n conversations, each with `turns` queued turns)
//   (the child is this same file with --child)
import { spawn } from "node:child_process";
import { createInterface } from "node:readline";
import fs from "node:fs";
import path from "node:path";
import { args, freshDir, pct, report, sleep, startProvider } from "./common.mjs";

const a = args({ child: "", dir: "", url: "", submit: 0, n: 500, kills: 30, min: 300, max: 2000, tool: 300, latency: "200ms", restartLimit: -1, turns: 1 });

if (a.child) await child();
else await parent();

async function parent() {
	const provider = await startProvider({ latency: a.latency, jitter: "50ms", rounds: 2, limit: 100000 });
	const dir = freshDir("crash-pi-durable");
	const start = (submit) => {
		const extra = a.restartLimit >= 0 ? ["--restartLimit", String(a.restartLimit)] : [];
		const proc = spawn(process.execPath, ["--no-warnings", import.meta.filename, "--child", "1", "--dir", dir, "--url", provider.url, "--submit", String(submit), "--turns", String(a.turns), "--tool", String(a.tool), ...extra], {
			stdio: ["ignore", "pipe", "inherit"],
		});
		const lines = [];
		let wake;
		createInterface({ input: proc.stdout }).on("line", (l) => {
			lines.push(l);
			wake?.();
		});
		proc.on("exit", () => {
			lines.push("exit");
			wake?.();
		});
		const next = async (prefix) => {
			for (;;) {
				const i = lines.findIndex((l) => [prefix].flat().some((p) => l.startsWith(p)) || l === "exit");
				if (i >= 0) return lines.splice(i, 1)[0];
				await new Promise((r) => (wake = r));
			}
		};
		return { proc, next, lines };
	};
	let c = start(a.n);
	await c.next("ready");
	const restarts = [];
	const recoveries = [];
	let killed = 0;
	let drained = false;
	while (killed < a.kills && !drained) {
		const wait = a.min + Math.random() * (a.max - a.min);
		const at = Date.now();
		while (Date.now() - at < wait) {
			if (c.lines.some((l) => l.startsWith("drained") || l.startsWith("stuck"))) break;
			await sleep(20);
		}
		if (c.lines.some((l) => l.startsWith("drained") || l.startsWith("stuck"))) {
			drained = true;
			break;
		}
		c.proc.kill("SIGKILL");
		await new Promise((r) => (c.proc.exitCode !== null || c.proc.signalCode !== null ? r() : c.proc.once("exit", r)));
		killed++;
		const t = performance.now();
		c = start(0);
		const ready = await c.next("ready");
		restarts.push(performance.now() - t);
		recoveries.push(ready.replace("ready ", ""));
	}
	const final = await c.next(["drained", "stuck"]);
	await new Promise((r) => (c.proc.exitCode !== null ? r() : c.proc.once("exit", r)));

	// Check the stored outcome in a process that resumes nothing.
	const check = spawn(process.execPath, ["--no-warnings", import.meta.filename, "--child", "check", "--dir", dir, "--url", provider.url], { stdio: ["ignore", "pipe", "inherit"] });
	let summary = "";
	for await (const l of createInterface({ input: check.stdout })) summary += l;
	const ran = new Map();
	for (const t of fs.readFileSync(path.join(dir, "tools.log"), "utf8").split("\n").filter(Boolean).map((l) => l.split(" ")[0])) ran.set(t, (ran.get(t) ?? 0) + 1);
	const twice = [...ran.values()].filter((v) => v > 1).length;
	restarts.sort((x, y) => x - y);
	const stats = await provider.stats();
	report("crash harness=pi-durable", {
		conversations: a.n,
		turns: a.n * a.turns,
		kills: killed,
		restart_limit: a.restartLimit >= 0 ? a.restartLimit : "default(5)",
		end: final.split(" ")[0],
		...JSON.parse(summary),
		tool_runs: [...ran.values()].reduce((x, y) => x + y, 0),
		calls_run_twice: twice,
		restart_p50_ms: pct(restarts, 0.5),
		restart_max_ms: pct(restarts, 1),
		requests: stats.requests,
	});
	console.log("recoveries:", recoveries.join(" | "));
	provider.stop();
	process.exit(0);
}

async function child() {
	const { openDurable, ctx } = await import("./pi-durable.mjs");
	const startAt = performance.now();
	const settings = a.restartLimit >= 0 ? { restartLimit: a.restartLimit === 0 ? Infinity : a.restartLimit } : {};
	const d = await openDurable({
		file: path.join(a.dir, "pi.sqlite"),
		url: a.url,
		toolMs: a.tool,
		log: a.child === "check" ? undefined : path.join(a.dir, "tools.log"),
		settings,
		onReport: (err, info) => {
			if (a.child !== "check" && info.type !== "task_parked") console.error("report", info.type, err?.message ?? err);
		},
	});
	const openMs = performance.now() - startAt;
	const convFile = path.join(a.dir, "convs.json");
	if (a.child === "check") {
		const convs = JSON.parse(fs.readFileSync(convFile, "utf8"));
		const status = {};
		let interrupted = 0;
		let toolErrors = 0;
		const seen = new Set();
		for (const { conv, sub } of convs) {
			const s = await (await d.harness.submission(sub, ctx)).status(ctx);
			const k = s.status === "unanswered" ? `unanswered:${s.reason}` : s.status;
			status[k] = (status[k] ?? 0) + 1;
			if (seen.has(conv)) continue;
			seen.add(conv);
			const view = await (await d.conversation(conv)).context(ctx);
			for (const e of view.entries) {
				if (e.kind !== "pi.tool-result") continue;
				const m = e.model?.[0];
				if (m?.isError) {
					toolErrors++;
					if (JSON.stringify(e).includes("nterrupt")) interrupted++;
				}
			}
		}
		const ins = await d.harness.inspect(ctx);
		const blocked = ins.tasks.filter((t) => t.state.kind === "blocked").length;
		console.log(JSON.stringify({ submissions: JSON.stringify(status).replaceAll(" ", ""), open_submissions: ins.submissions.length, live_tasks: ins.tasks.length, blocked_tasks: blocked, tool_error_results: toolErrors, interrupted_results: interrupted }));
		await d.close();
		process.exit(0);
	}
	d.harness.resume();
	if (a.submit > 0) {
		const convs = [];
		for (let i = 0; i < a.submit; i++) {
			const conv = await d.newConversation();
			// Turns after the first queue as follow-ups and run one after another.
			for (let j = 0; j < a.turns; j++) {
				const sub = await conv.submit({ type: "input", content: "work", requestId: `turn-${i}-${j}` }, ctx);
				convs.push({ conv: conv.id, sub: sub.id });
			}
		}
		fs.writeFileSync(convFile, JSON.stringify(convs));
	}
	const pending = (await d.harness.inspect(ctx)).submissions.length;
	console.log(`ready open_ms=${openMs.toFixed(0)} open_submissions=${pending}`);
	for (;;) {
		const ins = await d.harness.inspect(ctx);
		if (ins.submissions.length === 0) {
			console.log("drained");
			break;
		}
		if (ins.tasks.length > 0 && ins.tasks.every((t) => t.state.kind === "blocked" || t.state.kind === "waiting")) {
			// Nothing can run: every live task is parked or waits on one that is.
			console.log(`stuck blocked=${ins.tasks.filter((t) => t.state.kind === "blocked").length} open_submissions=${ins.submissions.length}`);
			break;
		}
		await sleep(200);
	}
	await d.close();
	process.exit(0);
}
