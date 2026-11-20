import path from "node:path";
import { args, footprintMB, freshDir, pct, report, sleep, startProvider } from "./common.mjs";

const a = args({ harness: "ollupi", n: 200, seconds: 20, latency: "1s", jitter: "200ms", kill: 0, wait: 60 });
const calls = {
	ollupi: ["run_command", JSON.stringify({ program: "sleep", args: [String(a.seconds)], background: true })],
	dsh: ["bash", JSON.stringify({ description: "Sleep", command: `sleep ${a.seconds}`, run_in_background: true })],
};
if (!calls[a.harness]) throw new Error(`background runs on ${Object.keys(calls).join(" or ")}`);
const [call, callArgs] = calls[a.harness];
const provider = await startProvider({ latency: a.latency, jitter: a.jitter, rounds: 1, limit: 100000, call, callArgs });
const dir = freshDir(`background-${a.harness}${a.kill ? "-kill" : ""}`);
const finals = new Map();
const h = a.harness === "ollupi" ? await ollupi() : await dsh();

const ids = [];
for (let i = 0; i < a.n; i++) ids.push(await h.newSession());
const base = footprintMB(h.pid());
const startAt = performance.now();
const started = new Map();
await Promise.all(
	ids.map(async (id) => {
		started.set(id, performance.now());
		await h.start(id);
	}),
);
const submitted = (performance.now() - startAt) / 1000;
let peak = footprintMB(h.pid());
const sampler = setInterval(() => (peak = Math.max(peak, footprintMB(h.pid()) || 0)), 2000);
let killedAt;
if (a.kill > 0) {
	await sleep(a.kill * 1000);
	killedAt = (performance.now() - startAt) / 1000;
	await h.restart();
}
const deadline = performance.now() + (a.seconds + a.wait) * 1000;
while (finals.size < ids.length && performance.now() < deadline) await sleep(100);
clearInterval(sampler);
const delays = [...finals].map(([id, at]) => (at - started.get(id)) / 1000 - a.seconds).sort((x, y) => x - y);
const stats = await provider.stats();
report(`background harness=${a.harness}`, {
	n: a.n,
	seconds: a.seconds,
	kill_at_s: killedAt ?? "none",
	submit_s: submitted,
	finished: finals.size,
	never_finished: ids.length - finals.size,
	over_seconds_p50_s: pct(delays, 0.5),
	over_seconds_p99_s: pct(delays, 0.99),
	footprint_base_mb: base,
	footprint_peak_mb: peak,
	footprint_kb_per_job: ((peak - base) * 1024) / a.n,
	requests: stats.requests,
	...(await h.summary()),
});
await h.close();
provider.stop();
process.exit(0);

async function ollupi() {
	const { connect, startServe } = await import("./ollupi/client.mjs");
	const home = path.join(dir, "ollupi-home");
	const settings = { calls: 1024, slots: 1024, taskSlots: a.n, userTaskSlots: a.n };
	let serve = await startServe({ dir: home, url: provider.url, settings });
	let c = await connect(serve.url);
	const turns = new Map();
	const watch = (id, threadId, client) =>
		client.waitTurn(id).then((t) => {
			if (client === c) finals.set(threadId, performance.now());
		});
	return {
		pid: () => serve.proc.pid,
		newSession: () => c.newThread(),
		async start(threadId) {
			const id = await c.startTurn(threadId, "bg");
			turns.set(threadId, id);
			watch(id, threadId, c);
		},
		async restart() {
			await serve.kill();
			serve = await startServe({ dir: home, url: provider.url, settings });
			c = await connect(serve.url);
			for (const [threadId, id] of turns) {
				if (finals.has(threadId)) continue;
				watch(id, threadId, c);
				const view = await c.call("thread/read", { threadId });
				for (const t of view.turns) c.settle(t);
			}
		},
		async summary() {
			let cutOff = 0;
			for (const threadId of turns.keys()) {
				const view = await c.call("thread/read", { threadId });
				cutOff += view.items.filter((it) => it.type === "agentReport" && it.status === "failed").length;
			}
			return { reports_cut_off: cutOff };
		},
		async close() {
			c.close();
			await serve.stop();
		},
	};
}

async function dsh() {
	const { startDsh } = await import("./dsh/client.mjs");
	const home = path.join(dir, "dsh-home");
	const cwd = path.join(dir, "cwd");
	const ends = new Map();
	const onNotify = (method, params) => {
		if (method !== "session.event" || params?.event?.type !== "turn/end") return;
		const n = (ends.get(params.sessionId) ?? 0) + 1;
		ends.set(params.sessionId, n);
		if (n >= 2 && !finals.has(params.sessionId)) finals.set(params.sessionId, performance.now());
	};
	const open = () => startDsh({ home, cwd, url: provider.url, extraPatches: [path.join(import.meta.dirname, "dsh", "background.yml")], onNotify });
	let d = await open();
	let n = 0;
	return {
		pid: () => d.proc.pid,
		newSession: async () => `bg-${process.pid}-${++n}`,
		start: (id) => d.call("session/prompt", { sessionId: id, contentBlocks: [{ type: "text", text: "bg" }] }),
		async restart() {
			d.proc.kill("SIGKILL");
			await d.exited;
			d = await open();
			for (const id of ends.keys()) {
				if (finals.has(id)) continue;
				ends.set(id, 1);
				await d.call("session/resume", { sessionId: id }).catch(() => {});
			}
		},
		summary: async () => ({}),
		close: () => d.close(),
	};
}
