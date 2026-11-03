// Experiment 3, burst against a provider's concurrency limit: n turns submitted at once, each in its own session,
// against a provider that refuses requests beyond `limit` in flight with 429. Neither pi harness has a client-side
// limit on model calls in flight, so what happens is whatever its retry policy does with the 429s.
//
//   node --expose-gc burst.mjs --harness pi-sdk|pi-durable|dsh --n 3000 --limit 256
import { args, cpuSeconds, freshDir, pct, peakSampler, report, secs, startProvider } from "./common.mjs";
import { openHarness, pool } from "./harnesses.mjs";

const a = args({ harness: "pi-sdk", n: 3000, limit: 256, latency: "5s", jitter: "2s", tool: 1000, retries: -1 });
const provider = await startProvider({ latency: a.latency, jitter: a.jitter, rounds: 2, limit: a.limit });
const dir = freshDir(`burst-${a.harness}-${a.n}`);
const settings = a.retries >= 0 ? { retry: { maxRetries: a.retries } } : {};
const h = await openHarness(a.harness, { dir, url: provider.url, toolMs: a.tool, settings });
const sessions = await pool(Array.from({ length: a.n }), 64, () => h.newSession());

const stopPeak = peakSampler(2000, h.pid);
const cpuAt = h.cpu();
const startAt = performance.now();
const results = await Promise.all(sessions.map((s) => h.turn(s, "work")));
const total = (performance.now() - startAt) / 1000;
const cpu = h.cpu() - cpuAt;
const peak = stopPeak();
const stats = await provider.stats();
const ok = results.filter((r) => !r.failed).map((r) => r.ms / 1000).sort((x, y) => x - y);
const errors = new Map();
for (const r of results.filter((r) => r.failed)) {
	const k = String(r.error ?? r.reason).slice(0, 80);
	errors.set(k, (errors.get(k) ?? 0) + 1);
}
const bound = (a.n * 3 * secs(a.latency)) / a.limit;
report(`burst harness=${a.harness}`, {
	n: a.n,
	limit: a.limit,
	retries: a.retries >= 0 ? a.retries : "default",
	total_s: total,
	bound_s: bound,
	total_over_bound: total / bound,
	completed_turns: ok.length,
	failed_turns: a.n - ok.length,
	p50_s: pct(ok, 0.5),
	p95_s: pct(ok, 0.95),
	p99_s: pct(ok, 0.99),
	...stats,
	rss_peak_mb: peak.rss_mb,
	footprint_peak_mb: peak.footprint_mb,
	cpu_ms_per_turn: (cpu * 1000) / a.n,
	errors: JSON.stringify([...errors]).replaceAll(" ", "_"),
});
await h.close();
provider.stop();
process.exit(0);
