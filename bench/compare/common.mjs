// Shared helpers for the comparison benches: the fake provider (ollupi's own `bench provider`), memory and
// latency statistics, and the token log that shows a tool call running twice.
import { spawn, execFileSync } from "node:child_process";
import { createInterface } from "node:readline";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

export const here = path.dirname(fileURLToPath(import.meta.url));
export const runDir = path.join(here, "run");
export const benchBin = process.env.OLLUPI_BENCH ?? path.join(runDir, "bench");

// startProvider runs ollupi's fake OpenAI-compatible provider in its own process, so what it holds is not counted
// against the harness: each turn's first `rounds` requests are answered with a bench__work call, then "done".
export async function startProvider({ latency = "5s", jitter = "2s", rounds = 2, limit = 1024 } = {}) {
	const proc = spawn(benchBin, ["provider", "-latency", latency, "-jitter", jitter, "-rounds", String(rounds), "-limit", String(limit)], {
		stdio: ["ignore", "pipe", "inherit"],
	});
	const url = await new Promise((resolve, reject) => {
		const rl = createInterface({ input: proc.stdout });
		rl.once("line", (line) => resolve(line.trim()));
		proc.once("exit", (code) => reject(new Error(`provider exited ${code}`)));
	});
	return {
		url,
		proc,
		async stats() {
			const [peak, refused, requests] = (await (await fetch(`${url}/stats`)).text()).trim().split(/\s+/).map(Number);
			return { peak_in_flight: peak, refused_429: refused, requests };
		},
		stop() {
			proc.kill();
		},
	};
}

export const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

export function rssMB(pid = process.pid) {
	if (pid === process.pid) return process.memoryUsage().rss / 2 ** 20;
	try {
		return Number(execFileSync("ps", ["-o", "rss=", "-p", String(pid)]).toString().trim()) / 1024;
	} catch {
		return -1;
	}
}

// footprintMB reads macOS's phys_footprint (what Activity Monitor calls Memory): unlike ps's RSS it keeps counting
// dirty pages the kernel compressed or swapped out, which RSS drops when the machine is short of memory.
export function footprintMB(pid = process.pid) {
	try {
		const out = execFileSync("footprint", [String(pid)], { stdio: ["ignore", "pipe", "ignore"] }).toString();
		const m = out.match(/Footprint:\s*([\d.]+)\s*(KB|MB|GB|B)/);
		return Number(m[1]) * { B: 1 / 2 ** 20, KB: 1 / 1024, MB: 1, GB: 1024 }[m[2]];
	} catch {
		return NaN;
	}
}

// settledMemory forces collections first, when node runs with --expose-gc, so retained memory is what remains.
export async function settledMemory() {
	for (let i = 0; i < 3; i++) {
		globalThis.gc?.();
		await sleep(200);
	}
	const m = process.memoryUsage();
	return { rss_mb: m.rss / 2 ** 20, footprint_mb: footprintMB(), heap_mb: m.heapUsed / 2 ** 20, external_mb: (m.external + m.arrayBuffers) / 2 ** 20 };
}

export function pct(sorted, p) {
	if (sorted.length === 0) return 0;
	return sorted[Math.min(sorted.length - 1, Math.floor(sorted.length * p))];
}

export function dirBytes(dir) {
	let size = 0;
	if (!fs.existsSync(dir)) return 0;
	for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
		const p = path.join(dir, e.name);
		size += e.isDirectory() ? dirBytes(p) : fs.statSync(p).size;
	}
	return size;
}

export function cpuSeconds() {
	const u = process.cpuUsage();
	return (u.user + u.system) / 1e6;
}

// peakSampler records the process's peak resident memory and footprint every interval until stopped.
export function peakSampler(intervalMs = 2000, pid = process.pid) {
	const peak = { rss_mb: rssMB(pid), footprint_mb: footprintMB(pid) };
	const sample = () => {
		peak.rss_mb = Math.max(peak.rss_mb, rssMB(pid));
		const f = footprintMB(pid);
		if (!Number.isNaN(f)) peak.footprint_mb = Math.max(peak.footprint_mb || 0, f);
	};
	const t = setInterval(sample, intervalMs);
	return () => {
		clearInterval(t);
		sample();
		return peak;
	};
}

export function freshDir(name) {
	const dir = path.join(runDir, name);
	fs.rmSync(dir, { recursive: true, force: true });
	fs.mkdirSync(dir, { recursive: true });
	return dir;
}

export function args(defaults) {
	const out = { ...defaults };
	const argv = process.argv.slice(2);
	for (let i = 0; i < argv.length; i++) {
		const m = argv[i].match(/^--?([\w-]+)(?:=(.*))?$/);
		if (!m) continue;
		const key = m[1];
		let val = m[2];
		if (val === undefined) val = argv[i + 1] !== undefined && !argv[i + 1].startsWith("-") ? argv[++i] : "true";
		out[key] = typeof defaults[key] === "number" ? Number(val) : val;
	}
	return out;
}

export function report(name, fields) {
	const line = `${name} ` + Object.entries(fields).map(([k, v]) => `${k}=${typeof v === "number" && !Number.isInteger(v) ? v.toFixed(2) : v}`).join(" ");
	console.log(line);
	fs.mkdirSync(path.join(here, "results"), { recursive: true });
	fs.appendFileSync(path.join(here, "results", "results.txt"), `${new Date().toISOString()} ${line}\n`);
	return line;
}

// secs parses a Go duration such as "5s" or "200ms" into seconds.
export function secs(d) {
	const m = String(d).match(/^([\d.]+)(ms|s)$/);
	return m[2] === "ms" ? Number(m[1]) / 1000 : Number(m[1]);
}

// cpuSecondsOf reads another process's accumulated CPU time from ps (formats M:SS.ss or H:MM:SS).
export function cpuSecondsOf(pid) {
	try {
		const t = execFileSync("ps", ["-o", "time=", "-p", String(pid)]).toString().trim();
		return t.split(":").reduce((acc, part) => acc * 60 + Number(part), 0);
	} catch {
		return 0;
	}
}
