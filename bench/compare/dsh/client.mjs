// A minimal client of dsh's SDK runtime (`dsh --profile sdk-minimal`), which speaks newline-delimited JSON-RPC 2.0 on
// stdio: initialize once, then session/prompt (an unknown sessionId creates the session) and session/wait per turn.
import { spawn } from "node:child_process";
import { createInterface } from "node:readline";
import fs from "node:fs";
import path from "node:path";
import { here } from "../common.mjs";

const dshBin = path.join(here, "node_modules", ".bin", "dsh");
const patch = path.join(here, "dsh", "patch.yml");

export async function startDsh({ home, cwd, url, toolMs = 1000, log, extraPatches = [], onNotify, env = {} }) {
	fs.mkdirSync(home, { recursive: true });
	fs.mkdirSync(cwd, { recursive: true });
	const proc = spawn(dshBin, ["--profile", "sdk-minimal", "--patch", patch, ...extraPatches.flatMap((p) => ["--patch", p])], {
		cwd,
		env: {
			...process.env,
			DSH_HOME: home,
			BENCH_PROVIDER_URL: url,
			BENCH_API_KEY: "dummy",
			BENCH_TOOL_MS: String(toolMs),
			...(log ? { BENCH_TOOL_LOG: log } : {}),
			...env,
		},
		stdio: ["pipe", "pipe", "pipe"],
	});
	const stderr = [];
	proc.stderr.on("data", (d) => {
		stderr.push(d.toString());
		if (stderr.length > 200) stderr.shift();
	});
	let next = 1;
	const pending = new Map();
	// session/wait answers {} however the turn ended; the turn/end event, which precedes that answer, says how.
	const lastEnd = new Map();
	createInterface({ input: proc.stdout }).on("line", (line) => {
		let msg;
		try {
			msg = JSON.parse(line);
		} catch {
			return;
		}
		if (msg.id !== undefined && pending.has(msg.id)) {
			const { resolve, reject } = pending.get(msg.id);
			pending.delete(msg.id);
			if (msg.error) reject(Object.assign(new Error(msg.error.message), { code: msg.error.code, data: msg.error.data }));
			else resolve(msg.result);
		} else if (msg.method) {
			if (msg.method === "session.event" && msg.params?.event?.type === "turn/end") lastEnd.set(msg.params.sessionId, msg.params.event.data?.reason);
			onNotify?.(msg.method, msg.params);
		}
	});
	const exited = new Promise((r) => proc.once("exit", (code, signal) => r({ code, signal })));
	exited.then(({ code, signal }) => {
		for (const { reject } of pending.values()) reject(new Error(`dsh exited ${code ?? signal}: ${stderr.join("").slice(-2000)}`));
		pending.clear();
	});
	const call = (method, params) =>
		new Promise((resolve, reject) => {
			const id = next++;
			pending.set(id, { resolve, reject });
			proc.stdin.write(JSON.stringify({ jsonrpc: "2.0", id, method, params }) + "\n");
		});
	const at = performance.now();
	await call("initialize", { cwd, provider: "fake", model: "fake" });
	return {
		proc,
		initMs: performance.now() - at,
		call,
		exited,
		stderr: () => stderr.join(""),
		async turn(sessionId, text = "work") {
			const t = performance.now();
			try {
				lastEnd.delete(sessionId);
				await call("session/prompt", { sessionId, contentBlocks: [{ type: "text", text }] });
				await call("session/wait", { sessionId });
				const reason = lastEnd.get(sessionId);
				const failed = reason?.kind !== "completed";
				return { ms: performance.now() - t, failed, error: failed ? (reason?.error?.code ?? reason?.kind ?? "no turn/end") : undefined };
			} catch (e) {
				return { ms: performance.now() - t, failed: true, error: e.message };
			}
		},
		async close() {
			try {
				await Promise.race([call("shutdown", undefined), new Promise((r) => setTimeout(r, 5000))]);
			} catch {}
			proc.stdin.end();
			const t = setTimeout(() => proc.kill("SIGKILL"), 10000);
			await exited;
			clearTimeout(t);
		},
	};
}
