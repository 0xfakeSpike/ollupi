import { spawn } from "node:child_process";
import { createInterface } from "node:readline";
import fs from "node:fs";
import { benchBin } from "../common.mjs";

const ended = new Set(["completed", "failed", "interrupted"]);

export async function startServe({ dir, url, toolMs = 1000, log, settings = {} }) {
	fs.mkdirSync(dir, { recursive: true });
	const flags = {
		dir,
		url,
		tool: `${toolMs}ms`,
		calls: settings.calls ?? 1024,
		slots: settings.slots ?? 1024,
		"task-slots": settings.taskSlots ?? 32,
		"user-task-slots": settings.userTaskSlots ?? 4,
		...(log ? { "tool-log": log } : {}),
	};
	const proc = spawn(benchBin, ["serve", ...Object.entries(flags).flatMap(([k, v]) => [`-${k}`, String(v)])], {
		stdio: ["pipe", "pipe", "inherit"],
	});
	const lines = createInterface({ input: proc.stdout });
	const wsURL = await new Promise((resolve, reject) => {
		lines.once("line", (line) => resolve(line.trim()));
		proc.once("exit", (code, signal) => reject(new Error(`bench serve exited ${code ?? signal}`)));
	});
	const exited = new Promise((r) => proc.once("exit", (code, signal) => r({ code, signal })));
	const stats = [];
	lines.on("line", (line) => {
		if (line.startsWith("stats ")) stats.shift()?.(Object.fromEntries(line.slice(6).split(" ").map((kv) => kv.split("=")).map(([k, v]) => [k, Number(v)])));
	});
	return {
		proc,
		url: wsURL,
		exited,
		stats: () =>
			new Promise((resolve) => {
				stats.push(resolve);
				proc.stdin.write("stats\n");
			}),
		kill() {
			proc.kill("SIGKILL");
			return exited;
		},
		async stop() {
			proc.stdin.end();
			const t = setTimeout(() => proc.kill("SIGKILL"), 10000);
			await exited;
			clearTimeout(t);
		},
	};
}

async function open(url, user) {
	for (let wait = 200; ; wait = Math.min(wait * 2, 5000)) {
		const ws = new WebSocket(url, ["ollupi.token", user]);
		const opened = await new Promise((resolve) => {
			ws.addEventListener("open", () => resolve(true), { once: true });
			ws.addEventListener("error", () => resolve(false), { once: true });
		});
		if (opened) return ws;
		await new Promise((r) => setTimeout(r, wait * (0.5 + Math.random())));
	}
}

export async function connect(url, user = "user000") {
	const ws = await open(url, user);
	let next = 1;
	const pending = new Map();
	const turns = new Map();
	const waiters = new Map();
	const closed = new Promise((r) => ws.addEventListener("close", (e) => r(e.code), { once: true }));
	closed.then((code) => {
		for (const { reject } of pending.values()) reject(new Error(`the connection closed with ${code}`));
		pending.clear();
		for (const [id, ws] of waiters) for (const w of ws) w({ id, status: "closed", error: `the connection closed with ${code}` });
		waiters.clear();
	});
	const settle = (turn) => {
		turns.set(turn.id, turn);
		if (!ended.has(turn.status)) return;
		for (const w of waiters.get(turn.id) ?? []) w(turn);
		waiters.delete(turn.id);
	};
	ws.addEventListener("message", (e) => {
		const msg = JSON.parse(e.data);
		if (msg.id !== undefined && pending.has(msg.id)) {
			const { resolve, reject } = pending.get(msg.id);
			pending.delete(msg.id);
			if (msg.error) reject(Object.assign(new Error(msg.error.message), { code: msg.error.code, data: msg.error.data }));
			else resolve(msg.result);
		} else if (msg.method === "turn/changed") {
			settle(msg.params.turn);
		}
	});
	let shut = false;
	closed.then(() => (shut = true));
	const call = (method, params) =>
		new Promise((resolve, reject) => {
			if (shut) return reject(new Error("the connection is closed"));
			const id = next++;
			pending.set(id, { resolve, reject });
			ws.send(JSON.stringify({ jsonrpc: "2.0", id, method, params }));
		});
	await call("initialize", { clientInfo: { name: "bench", version: "1" } });
	ws.send(JSON.stringify({ jsonrpc: "2.0", method: "initialized", params: {} }));
	const waitTurn = (id) =>
		new Promise((resolve) => {
			const t = turns.get(id);
			if (t && ended.has(t.status)) return resolve(t);
			waiters.set(id, [...(waiters.get(id) ?? []), resolve]);
		});
	return {
		call,
		closed,
		close: () => ws.close(1000),
		settle,
		waitTurn,
		async newThread() {
			return (await call("thread/start", { title: "bench" })).thread.id;
		},
		async startTurn(threadId, text) {
			return (await call("turn/start", { threadId, input: [{ type: "text", text }] })).turn.id;
		},
		async turn(threadId, text = "work") {
			const at = performance.now();
			try {
				const id = await this.startTurn(threadId, text);
				const t = await waitTurn(id);
				const failed = t.status !== "completed";
				return { ms: performance.now() - at, failed, error: failed ? t.error || t.status : undefined, id };
			} catch (e) {
				return { ms: performance.now() - at, failed: true, error: e.message };
			}
		},
	};
}
