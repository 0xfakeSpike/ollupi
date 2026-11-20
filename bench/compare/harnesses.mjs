import fs from "node:fs";
import path from "node:path";
import { cpuSeconds, cpuSecondsOf, dirBytes, footprintMB, rssMB, settledMemory, sleep } from "./common.mjs";

export async function openHarness(name, { dir, url, toolMs = 1000, log, settings = {} }) {
	if (name === "pi-sdk") {
		const { openPi, runTurn } = await import("./pi-sdk.mjs");
		const pi = await openPi({ dir, url, toolMs, log, retry: settings.retry });
		return {
			name,
			newSession: () => pi.newSession(),
			turn: (s, text) => runTurn(s, text),
			idOf: (s) => s.sessionFile,
			drop: (s) => s.dispose(),
			reopen: (file) => pi.openSession(file),
			diskBytes: () => dirBytes(pi.sessionDir),
			memory: settledMemory,
			cpu: cpuSeconds,
			pid: process.pid,
			close: async () => {},
		};
	}
	if (name === "pi-durable") {
		const { openDurable, runTurn } = await import("./pi-durable.mjs");
		const file = path.join(dir, "pi.sqlite");
		const d = await openDurable({ file, url, toolMs, log, settings });
		return {
			name,
			harness: d.harness,
			newSession: () => d.newConversation(),
			turn: (c, text) => runTurn(c, text),
			idOf: (c) => c.id,
			drop: () => {},
			reopen: (id) => d.conversation(id),
			diskBytes: () => ["", "-wal", "-shm"].reduce((n, s) => n + (fs.existsSync(file + s) ? fs.statSync(file + s).size : 0), 0),
			memory: settledMemory,
			cpu: cpuSeconds,
			pid: process.pid,
			close: () => d.close(),
		};
	}
	if (name === "ollupi") {
		const { startServe, connect } = await import("./ollupi/client.mjs");
		const home = path.join(dir, "ollupi-home");
		const serve = await startServe({ dir: home, url, toolMs, log, settings: settings.ollupi });
		const clients = new Map();
		const client = (user) => {
			if (!clients.has(user)) clients.set(user, connect(serve.url, user));
			return clients.get(user);
		};
		const perUser = settings.perUser ?? 64;
		let made = 0;
		return {
			name,
			serve,
			newSession: async () => {
				const user = `user${String(Math.floor(made++ / perUser)).padStart(6, "0")}`;
				const c = await client(user);
				return { user, c, id: await c.newThread() };
			},
			turn: (s, text) => s.c.turn(s.id, text),
			idOf: (s) => ({ user: s.user, id: s.id }),
			drop: () => {},
			reopen: async ({ user, id }) => {
				const c = await client(user);
				await c.call("thread/resume", { threadId: id });
				return { user, c, id };
			},
			diskBytes: () => dirBytes(path.join(home, "data")),
			memory: async () => {
				await sleep(1000);
				const s = await serve.stats();
				return { rss_mb: rssMB(serve.proc.pid), footprint_mb: footprintMB(serve.proc.pid), heap_mb: s.heap_mb };
			},
			cpu: () => cpuSecondsOf(serve.proc.pid),
			pid: serve.proc.pid,
			close: async () => {
				for (const c of clients.values()) (await c).close();
				await serve.stop();
			},
		};
	}
	if (name === "dsh") {
		const { startDsh } = await import("./dsh/client.mjs");
		const home = path.join(dir, "dsh-home");
		const cwd = path.join(dir, "cwd");
		const d = await startDsh({ home, cwd, url, toolMs, log, env: settings.env ?? {} });
		let n = 0;
		return {
			name,
			dsh: d,
			newSession: async () => `s-${process.pid}-${Date.now().toString(36)}-${++n}`,
			turn: (id, text) => d.turn(id, text),
			idOf: (id) => id,
			drop: () => {},
			reopen: async (id) => {
				await d.call("session/resume", { sessionId: id });
				return id;
			},
			diskBytes: () => dirBytes(path.join(home, "sessions")),
			memory: async () => {
				await sleep(1000);
				return { rss_mb: rssMB(d.proc.pid), footprint_mb: footprintMB(d.proc.pid), heap_mb: NaN };
			},
			cpu: () => cpuSecondsOf(d.proc.pid),
			pid: d.proc.pid,
			close: () => d.close(),
		};
	}
	throw new Error(`unknown harness ${name}`);
}

export async function pool(items, n, fn) {
	let next = 0;
	const out = new Array(items.length);
	await Promise.all(
		Array.from({ length: Math.min(n, items.length) }, async () => {
			while (next < items.length) {
				const i = next++;
				out[i] = await fn(items[i], i);
			}
		}),
	);
	return out;
}
