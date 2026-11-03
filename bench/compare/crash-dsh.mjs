// Experiment 4c, crash (dsh): one dsh SDK runtime runs n sessions of `turns` turns each; the parent kill -9s the
// runtime at random moments, starts it again on the same DSH_HOME and session/resume's every session. In --mode closed
// each session sends its next prompt when the last turn ended (and after a crash goes on with the new runtime); in
// --mode queued every prompt is enqueued up front and the runtime is expected to work through them. At the end it reads every session log and reports how many
// turns ended and how, which bench__work calls ran twice (each call writes agent/call id to tools.log first), how many
// calls got no result, and how long a restart took. Model 200 ms, tool 300 ms, as ollupi's `bench crash`.
//
//   node crash-dsh.mjs --mode closed --n 50 --turns 20 --kills 30
//   node crash-dsh.mjs --mode queued --n 50 --turns 20 --kills 30
import fs from "node:fs";
import path from "node:path";
import { args, freshDir, pct, report, sleep, startProvider } from "./common.mjs";
import { startDsh } from "./dsh/client.mjs";

const a = args({ n: 50, turns: 20, kills: 30, min: 300, max: 2000, tool: 300, mode: "closed" });
const provider = await startProvider({ latency: "200ms", jitter: "50ms", rounds: 2, limit: 100000 });
const dir = freshDir(`crash-dsh-${a.mode}`);
const home = path.join(dir, "dsh-home");
const cwd = path.join(dir, "cwd");
const log = path.join(dir, "tools.log");
const ids = Array.from({ length: a.n }, (_, i) => `crash-${i}`);
const start = () => startDsh({ home, cwd, url: provider.url, toolMs: a.tool, log });

let d = await start();
const restarts = [];
const resumeErrors = {};
let killed = 0;
let waitErrors = 0;
let sentPrompts = 0;

if (a.mode === "queued") {
	// Every session's prompts are enqueued at once; the runtime runs them one after another.
	for (const id of ids) for (let j = 0; j < a.turns; j++) await d.call("session/prompt", { sessionId: id, contentBlocks: [{ type: "text", text: "work" }] });
	sentPrompts = a.n * a.turns;
	const waitAll = (rt) => Promise.allSettled(ids.map((id) => rt.call("session/wait", { sessionId: id })));
	let waits = waitAll(d);
	let done = false;
	waits.then(() => (done = true));
	while (killed < a.kills && !done) {
		const until = Date.now() + a.min + Math.random() * (a.max - a.min);
		while (Date.now() < until && !done) await sleep(20);
		if (done) break;
		d.proc.kill("SIGKILL");
		await d.exited;
		await waits;
		killed++;
		d = await restart();
		done = false;
		waits = waitAll(d);
		waits.then(() => (done = true));
	}
	waitErrors = (await waits).filter((s) => s.status === "rejected").length;
} else {
	// Closed loop: each session sends its next prompt once the last turn ended, as a user would; after a crash it
	// carries on with the runtime that replaced the killed one.
	let swap;
	let next = new Promise((r) => (swap = r));
	let finished = 0;
	const workers = ids.map(async (id) => {
		let sent = 0;
		while (sent < a.turns) {
			const rt = d;
			try {
				await rt.call("session/prompt", { sessionId: id, contentBlocks: [{ type: "text", text: "work" }] });
				sent++;
				sentPrompts++;
				await rt.call("session/wait", { sessionId: id });
			} catch {
				if (rt === d) await next;
			}
		}
		finished++;
	});
	while (killed < a.kills && finished < ids.length) {
		const until = Date.now() + a.min + Math.random() * (a.max - a.min);
		while (Date.now() < until && finished < ids.length) await sleep(20);
		if (finished >= ids.length) break;
		d.proc.kill("SIGKILL");
		await d.exited;
		killed++;
		d = await restart();
		const s = swap;
		next = new Promise((r) => (swap = r));
		s();
	}
	await Promise.all(workers);
}
await d.close();

async function restart() {
	const at = performance.now();
	const rt = await start();
	for (const id of ids) {
		try {
			await rt.call("session/resume", { sessionId: id });
		} catch (e) {
			const k = e.message.slice(0, 60).replaceAll(" ", "_");
			resumeErrors[k] = (resumeErrors[k] ?? 0) + 1;
		}
	}
	restarts.push(performance.now() - at);
	return rt;
}

// Read what every session log recorded.
const ends = {};
let userPrompts = 0;
let toolCalls = 0;
let toolResults = 0;
let toolErrors = 0;
const sessionsRoot = path.join(home, "sessions");
const files = [];
for (const sub of fs.readdirSync(sessionsRoot)) for (const s of fs.readdirSync(path.join(sessionsRoot, sub))) files.push(path.join(sessionsRoot, sub, s, "session.v4.jsonl"));
for (const f of files) {
	if (!fs.existsSync(f)) continue;
	for (const line of fs.readFileSync(f, "utf8").split("\n")) {
		if (!line) continue;
		let e;
		try {
			e = JSON.parse(line);
		} catch {
			ends.torn_line = (ends.torn_line ?? 0) + 1;
			continue;
		}
		if (e.type === "agent/inbox/spliced" && e.data?.outcome === "canceled") {
			ends.prompts_canceled = (ends.prompts_canceled ?? 0) + (e.data.removedCount ?? 0);
		} else if (e.type === "turn/end") {
			const k = e.data?.reason?.kind ?? "?";
			ends[k] = (ends[k] ?? 0) + 1;
		} else if (e.type === "user/message" && e.data?.source?.kind === "user") userPrompts++;
		else if (e.type === "tool/call") toolCalls++;
		else if (e.type === "tool/result") {
			toolResults++;
			if (e.data?.message?.isError) toolErrors++;
		}
	}
}
const ran = new Map();
for (const t of fs.existsSync(log) ? fs.readFileSync(log, "utf8").split("\n").filter(Boolean) : []) ran.set(t, (ran.get(t) ?? 0) + 1);
restarts.sort((x, y) => x - y);
report("crash harness=dsh", {
	sessions: a.n,
	mode: a.mode,
	turns: a.n * a.turns,
	prompts_sent: sentPrompts,
	kills: killed,
	turn_ends: JSON.stringify(ends).replaceAll(" ", ""),
	user_prompts_placed: userPrompts,
	tool_calls: toolCalls,
	tool_results: toolResults,
	tool_error_results: toolErrors,
	tool_runs: [...ran.values()].reduce((x, y) => x + y, 0),
	// Executions the tool logged whose call the session log never recorded (the log's tail was lost with the process).
	runs_unknown_to_session_log: [...ran.values()].reduce((x, y) => x + y, 0) - toolCalls,
	// Prompts the runtime acknowledged that the session log neither placed nor canceled.
	prompts_acked_but_lost: sentPrompts - userPrompts - (ends.prompts_canceled ?? 0),
	calls_run_twice: [...ran.values()].filter((v) => v > 1).length,
	wait_errors_at_end: waitErrors,
	resume_errors: JSON.stringify(resumeErrors),
	restart_p50_ms: pct(restarts, 0.5),
	restart_max_ms: pct(restarts, 1),
	requests: (await provider.stats()).requests,
});
provider.stop();
process.exit(0);
