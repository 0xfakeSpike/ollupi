// Experiment 4b, what plain pi-coding-agent sessions do after kill -9: a child runs n sessions, each with `turns` turns
// back to back (model 200 ms, tool 300 ms); the parent kills it once mid-run, then a second process reopens every
// session file and reports where each one stopped, whether anything continues on its own, and what a new prompt
// to a cut-off session does.
//
//   node crash-sdk.mjs --n 200 --turns 5 --at 1500
import { spawn } from "node:child_process";
import { createInterface } from "node:readline";
import fs from "node:fs";
import path from "node:path";
import { args, freshDir, report, sleep, startProvider } from "./common.mjs";

const a = args({ child: "", dir: "", url: "", n: 200, turns: 5, at: 1500, tool: 300 });

if (a.child === "run") {
	const { openPi, runTurn } = await import("./pi-sdk.mjs");
	const pi = await openPi({ dir: a.dir, url: a.url, toolMs: a.tool, log: path.join(a.dir, "tools.log") });
	const sessions = [];
	for (let i = 0; i < a.n; i++) sessions.push(await pi.newSession());
	console.log("ready");
	await Promise.all(sessions.map(async (s) => {
		for (let j = 0; j < a.turns; j++) await runTurn(s, "work");
	}));
	console.log("finished");
	process.exit(0);
} else if (a.child === "reopen") {
	const { openPi, runTurn } = await import("./pi-sdk.mjs");
	const pi = await openPi({ dir: a.dir, url: a.url, toolMs: a.tool, log: path.join(a.dir, "tools.log") });
	const files = fs.readdirSync(path.join(a.dir, "sessions")).map((f) => path.join(a.dir, "sessions", f));
	const lastRole = {};
	const sessions = [];
	const at = performance.now();
	for (const f of files) {
		const s = await pi.openSession(f);
		sessions.push(s);
		const msgs = s.messages;
		const last = msgs[msgs.length - 1];
		const k = !last ? "empty" : last.role === "assistant" ? `assistant:${last.stopReason}` : last.role;
		lastRole[k] = (lastRole[k] ?? 0) + 1;
	}
	const reopenMs = performance.now() - at;
	const before = Number((await (await fetch(`${a.url}/stats`)).text()).split(" ")[2]);
	await sleep(3000);
	const after = Number((await (await fetch(`${a.url}/stats`)).text()).split(" ")[2]);
	// A new prompt to every session that stopped mid-turn.
	let ok = 0;
	let failed = 0;
	const errors = {};
	await Promise.all(
		sessions.map(async (s) => {
			const last = s.messages[s.messages.length - 1];
			if (last?.role === "assistant" && last.stopReason === "stop") return;
			const r = await runTurn(s, "work");
			if (r.failed) {
				failed++;
				const k = String(r.error).slice(0, 60).replaceAll(" ", "_");
				errors[k] = (errors[k] ?? 0) + 1;
			} else ok++;
		}),
	);
	console.log(JSON.stringify({ session_files: files.length, last_message: JSON.stringify(lastRole), reopen_all_ms: Math.round(reopenMs), requests_in_3s_after_reopen: after - before, reprompt_ok: ok, reprompt_failed: failed, reprompt_errors: JSON.stringify(errors) }));
	process.exit(0);
} else {
	const provider = await startProvider({ latency: "200ms", jitter: "50ms", rounds: 2, limit: 100000 });
	const dir = freshDir("crash-pi-sdk");
	const child = (mode) => spawn(process.execPath, ["--no-warnings", import.meta.filename, "--child", mode, "--dir", dir, "--url", provider.url, "--n", String(a.n), "--turns", String(a.turns), "--tool", String(a.tool)], { stdio: ["ignore", "pipe", "inherit"] });
	const run = child("run");
	const rl = createInterface({ input: run.stdout });
	await new Promise((r) => rl.on("line", (l) => l === "ready" && r()));
	await sleep(a.at);
	run.kill("SIGKILL");
	await new Promise((r) => run.once("exit", r));
	const startedCalls = fs.readFileSync(path.join(dir, "tools.log"), "utf8").split("\n").filter(Boolean).length;
	// Tool results stored: calls that finished and were written before the kill.
	let results = 0;
	for (const f of fs.readdirSync(path.join(dir, "sessions"))) {
		results += fs.readFileSync(path.join(dir, "sessions", f), "utf8").split("\n").filter((l) => l.includes('"role":"toolResult"')).length;
	}
	const re = child("reopen");
	let out = "";
	for await (const l of createInterface({ input: re.stdout })) out += l;
	report("crash harness=pi-sdk", {
		sessions: a.n,
		turns_each: a.turns,
		killed_at_ms: a.at,
		tool_calls_started_before_kill: startedCalls,
		tool_results_on_disk: results,
		calls_cut_off_without_result: startedCalls - results,
		...JSON.parse(out),
	});
	provider.stop();
	process.exit(0);
}
