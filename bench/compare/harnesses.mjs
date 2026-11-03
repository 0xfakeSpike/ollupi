// One interface over the harnesses compared, so each experiment is written once:
//   open({ dir, url, toolMs, log, settings }) -> { newSession, turn, idOf, drop, reopen, diskBytes, memory, cpu, pid, close }
// The pi harnesses run inside this process (memory is read after forced collections, so run node with --expose-gc);
// dsh runs as its own SDK runtime process, whose resident memory and CPU are read with ps.
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
			// A Conversation handle holds no state; what pi-durable keeps per conversation is governed by
			// settings.contextRetentionMs and documentRetentionMs (default ten minutes each).
			drop: () => {},
			reopen: (id) => d.conversation(id),
			diskBytes: () => ["", "-wal", "-shm"].reduce((n, s) => n + (fs.existsSync(file + s) ? fs.statSync(file + s).size : 0), 0),
			memory: settledMemory,
			cpu: cpuSeconds,
			pid: process.pid,
			close: () => d.close(),
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
			// A session id the runtime does not know creates the session at its first prompt.
			newSession: async () => `s-${process.pid}-${Date.now().toString(36)}-${++n}`,
			turn: (id, text) => d.turn(id, text),
			idOf: (id) => id,
			// The SDK protocol has no session close: a session stays resident until the process ends.
			drop: () => {},
			// session/resume restores a persisted session into a runtime that does not hold it yet.
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

// pool runs fn over items with at most n in flight.
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
