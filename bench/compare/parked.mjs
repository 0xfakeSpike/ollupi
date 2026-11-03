// Experiment 1, parked memory: n sessions that each ran one short turn and now sit idle. Reports the resident memory
// and JS heap they add per session, the disk they take per session, what dropping them from memory frees, and how long
// bringing one back from disk takes (reopen, then a turn against a 20 ms model).
//
//   node --expose-gc parked.mjs --harness pi-sdk|pi-durable|dsh --n 1000
import { args, freshDir, pct, report, startProvider } from "./common.mjs";
import { openHarness, pool } from "./harnesses.mjs";

const a = args({ harness: "pi-sdk", n: 1000, conc: 64, reload: 50, retention: "default" });
const provider = await startProvider({ latency: "20ms", jitter: "5ms", rounds: 2, limit: 100000 });
const dir = freshDir(`parked-${a.harness}`);
const settings = a.retention === "default" ? {} : { contextRetentionMs: Number(a.retention), documentRetentionMs: Number(a.retention) };
let h = await openHarness(a.harness, { dir, url: provider.url, toolMs: 10, settings });

// A warm-up session loads every lazily imported module before the baseline is taken.
const warm = await h.newSession();
await h.turn(warm, "work");
const base = await h.memory();

const setupAt = performance.now();
const sessions = await pool(Array.from({ length: a.n }), a.conc, async () => {
	const s = await h.newSession();
	const r = await h.turn(s, "work");
	if (r.failed) throw new Error(`setup turn failed: ${r.error ?? r.reason}`);
	return s;
});
const setupS = (performance.now() - setupAt) / 1000;
const ids = sessions.map(h.idOf);
const parked = await h.memory();
const disk = h.diskBytes();

// Drop every session from memory. pi-sdk: dispose each AgentSession. pi-durable: handles hold nothing, so close and
// reopen the Harness. dsh: the SDK has no session close, so restart the runtime process.
for (const s of sessions) h.drop(s);
sessions.length = 0;
let openMs = 0;
if (a.harness !== "pi-sdk") {
	await h.close();
	const at = performance.now();
	h = await openHarness(a.harness, { dir, url: provider.url, toolMs: 10, settings });
	openMs = performance.now() - at;
}
const dropped = await h.memory();
// After a close (pi-durable checkpoints its write-ahead log then), the disk the sessions take at rest.
const diskAtRest = h.diskBytes();

// Bring sessions back from disk: reopen, then one turn.
const pick = Array.from({ length: Math.min(a.reload, ids.length) }, (_, i) => ids[Math.floor((i * ids.length) / a.reload)]);
const reopenMs = [];
const turnMs = [];
for (const id of pick) {
	const at = performance.now();
	const s = await h.reopen(id);
	reopenMs.push(performance.now() - at);
	const r = await h.turn(s, "work");
	if (r.failed) throw new Error(`reload turn failed: ${r.error ?? r.reason}`);
	turnMs.push(r.ms);
	h.drop(s);
}
// The same turn on a session that stayed in memory, for comparison.
const warmMs = [];
for (let i = 0; i < 10; i++) warmMs.push((await h.turn(await h.newSession(), "work")).ms);
reopenMs.sort((x, y) => x - y);
turnMs.sort((x, y) => x - y);
warmMs.sort((x, y) => x - y);

report(`parked harness=${a.harness}`, {
	n: a.n,
	retention: a.retention,
	setup_s: setupS,
	rss_base_mb: base.rss_mb,
	rss_parked_mb: parked.rss_mb,
	rss_kb_per_session: ((parked.rss_mb - base.rss_mb) * 1024) / a.n,
	footprint_base_mb: base.footprint_mb,
	footprint_parked_mb: parked.footprint_mb,
	footprint_kb_per_session: ((parked.footprint_mb - base.footprint_mb) * 1024) / a.n,
	footprint_after_drop_mb: dropped.footprint_mb,
	heap_kb_per_session: ((parked.heap_mb - base.heap_mb) * 1024) / a.n,
	disk_kb_per_session_live: disk / 1024 / (a.n + 1),
	disk_kb_per_session_at_rest: diskAtRest / 1024 / (a.n + 1),
	rss_after_drop_mb: dropped.rss_mb,
	heap_after_drop_mb: dropped.heap_mb,
	heap_parked_mb: parked.heap_mb,
	reopen_harness_ms: openMs,
	reopen_session_p50_ms: pct(reopenMs, 0.5),
	reopen_session_p95_ms: pct(reopenMs, 0.95),
	first_turn_after_reload_p50_ms: pct(turnMs, 0.5),
	turn_new_session_p50_ms: pct(warmMs, 0.5),
});
await h.close();
provider.stop();
process.exit(0);
