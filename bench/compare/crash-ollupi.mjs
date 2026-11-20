import fs from "node:fs";
import path from "node:path";
import { args, freshDir, pct, report, sleep, startProvider } from "./common.mjs";
import { connect, startServe } from "./ollupi/client.mjs";

const a = args({ n: 50, turns: 20, kills: 30, min: 300, max: 2000, tool: 300 });
const provider = await startProvider({ latency: "200ms", jitter: "50ms", rounds: 2, limit: 100000 });
const dir = freshDir("crash-ollupi");
const home = path.join(dir, "ollupi-home");
const log = path.join(dir, "tools.log");
const start = async () => {
	const serve = await startServe({ dir: home, url: provider.url, toolMs: a.tool, log, settings: { calls: 64, slots: 32 } });
	return { serve, c: await connect(serve.url) };
};

let rt = await start();
let swap;
let next = new Promise((r) => (swap = r));
const threads = [];
for (let i = 0; i < a.n; i++) threads.push(await rt.c.newThread());

let finished = 0;
let sent = 0;
let lost = 0;
const workers = threads.map(async (threadId) => {
	for (let j = 0; j < a.turns; ) {
		const at = rt;
		let id;
		try {
			id = await at.c.startTurn(threadId, "work");
			sent++;
			j++;
		} catch {
			lost++;
			if (at === rt) await next;
			continue;
		}
		for (let cur = at; ; ) {
			const ended = await Promise.race([cur.c.waitTurn(id), cur.c.closed.then(() => undefined)]);
			if (ended && ended.status !== "closed") break;
			if (cur === rt) await next;
			cur = rt;
			const view = await cur.c.call("thread/read", { threadId }).catch(() => undefined);
			for (const t of view?.turns ?? []) cur.c.settle(t);
		}
	}
	finished++;
});

const restarts = [];
let killed = 0;
while (killed < a.kills && finished < threads.length) {
	const until = Date.now() + a.min + Math.random() * (a.max - a.min);
	while (Date.now() < until && finished < threads.length) await sleep(20);
	if (finished === threads.length) break;
	await rt.serve.kill();
	killed++;
	const t = performance.now();
	rt = await start();
	restarts.push(performance.now() - t);
	const was = swap;
	next = new Promise((r) => (swap = r));
	was();
}
await Promise.all(workers);

const statuses = {};
let open = 0;
for (const threadId of threads) {
	const view = await rt.c.call("thread/read", { threadId, limit: 500 });
	for (const t of view.turns) {
		statuses[t.status] = (statuses[t.status] ?? 0) + 1;
		if (t.status === "queued" || t.status === "inProgress") open++;
	}
}
const ran = new Map();
for (const t of fs.readFileSync(log, "utf8").split("\n").filter(Boolean)) ran.set(t, (ran.get(t) ?? 0) + 1);
restarts.sort((x, y) => x - y);
const stats = await provider.stats();
report("crash harness=ollupi", {
	conversations: a.n,
	turns_sent: sent,
	start_errors: lost,
	kills: killed,
	turns: JSON.stringify(statuses).replaceAll(" ", ""),
	open_turns: open,
	tool_runs: [...ran.values()].reduce((x, y) => x + y, 0),
	calls_run_twice: [...ran.values()].filter((v) => v > 1).length,
	restart_p50_ms: pct(restarts, 0.5),
	restart_max_ms: pct(restarts, 1),
	requests: stats.requests,
});
rt.c.close();
await rt.serve.stop();
provider.stop();
process.exit(0);
