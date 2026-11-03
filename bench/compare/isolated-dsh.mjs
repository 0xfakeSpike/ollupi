// The one-process-per-session baseline for dsh, as ollupi's `bench isolated`: k dsh SDK runtimes, each of which ran one
// short turn in its own session and now sits idle; reports each process's resident memory.
//
//   node isolated-dsh.mjs --k 10
import path from "node:path";
import { args, footprintMB, freshDir, report, rssMB, sleep, startProvider } from "./common.mjs";
import { startDsh } from "./dsh/client.mjs";

const a = args({ k: 10 });
const provider = await startProvider({ latency: "20ms", jitter: "5ms", rounds: 2, limit: 100000 });
const dir = freshDir("isolated-dsh");
const procs = [];
for (let i = 0; i < a.k; i++) {
	const d = await startDsh({ home: path.join(dir, `home-${i}`), cwd: path.join(dir, "cwd"), url: provider.url, toolMs: 10 });
	const r = await d.turn(`only-${i}`, "work");
	if (r.failed) throw new Error(r.error);
	procs.push(d);
}
await sleep(3000);
const rss = procs.map((d) => rssMB(d.proc.pid)).sort((x, y) => x - y);
const fp = procs.map((d) => footprintMB(d.proc.pid)).sort((x, y) => x - y);
report("isolated harness=dsh", {
	k: a.k,
	rss_mb_mean: rss.reduce((x, y) => x + y, 0) / rss.length,
	rss_mb_min: rss[0],
	rss_mb_median: rss[Math.floor(rss.length / 2)],
	rss_mb_max: rss[rss.length - 1],
	footprint_mb_mean: fp.reduce((x, y) => x + y, 0) / fp.length,
	footprint_mb_median: fp[Math.floor(fp.length / 2)],
});
await Promise.all(procs.map((d) => d.close()));
provider.stop();
process.exit(0);
