// Experiment 2, active memory and capacity: c sessions each run `turns` turns back to back against a model of
// 5 s ± 2 s (provider limit = c) and a 1 s tool, as ollupi's `bench steady` does. A turn alone takes about 17 s.
//
//   node --expose-gc steady.mjs --harness pi-sdk|pi-durable|dsh --c 256
import { args, cpuSeconds, freshDir, pct, peakSampler, report, secs, settledMemory, startProvider } from "./common.mjs";
import { openHarness, pool } from "./harnesses.mjs";

const a = args({ harness: "pi-sdk", c: 64, turns: 3, latency: "5s", jitter: "2s", tool: 1000, limit: 0 });
const limit = a.limit || a.c;
const provider = await startProvider({ latency: a.latency, jitter: a.jitter, rounds: 2, limit });
const dir = freshDir(`steady-${a.harness}-${a.c}`);
const h = await openHarness(a.harness, { dir, url: provider.url, toolMs: a.tool });

const sessions = await pool(Array.from({ length: a.c }), 64, () => h.newSession());
const base = await h.memory();
const stopPeak = peakSampler(2000, h.pid);
const cpuAt = h.cpu();
const startAt = performance.now();
const took = [];
let failed = 0;
const errors = new Map();
await Promise.all(
	sessions.map(async (s) => {
		for (let i = 0; i < a.turns; i++) {
			const r = await h.turn(s, "work");
			took.push(r.ms / 1000);
			if (r.failed) {
				failed++;
				const k = String(r.error ?? r.reason).slice(0, 80);
				errors.set(k, (errors.get(k) ?? 0) + 1);
			}
		}
	}),
);
const total = (performance.now() - startAt) / 1000;
const cpu = h.cpu() - cpuAt;
const peak = stopPeak();
const stats = await provider.stats();
took.sort((x, y) => x - y);
const latency = secs(a.latency);
const alone = 3 * latency + (2 * a.tool) / 1000;
const n = took.length;
report(`steady harness=${a.harness}`, {
	c: a.c,
	limit,
	turns: n,
	total_s: total,
	alone_s: alone,
	p50_s: pct(took, 0.5),
	p95_s: pct(took, 0.95),
	p99_s: pct(took, 0.99),
	p50_over_alone: pct(took, 0.5) / alone - 1,
	failed_turns: failed,
	...stats,
	rss_base_mb: base.rss_mb,
	rss_peak_mb: peak.rss_mb,
	footprint_base_mb: base.footprint_mb,
	footprint_peak_mb: peak.footprint_mb,
	active_kb_per_session_rss: ((peak.rss_mb - base.rss_mb) * 1024) / a.c,
	active_kb_per_session_footprint: ((peak.footprint_mb - base.footprint_mb) * 1024) / a.c,
	cpu_cores: cpu / total,
	cpu_ms_per_turn: (cpu * 1000) / n,
	disk_kb_per_turn_live: h.diskBytes() / 1024 / n,
	errors: JSON.stringify([...errors]).replaceAll(" ", "_"),
});
await h.close();
report(`steady-disk harness=${a.harness}`, { c: a.c, disk_kb_per_turn_after_close: h.diskBytes() / 1024 / n });
provider.stop();
process.exit(0);
