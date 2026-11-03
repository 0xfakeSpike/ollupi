// A Cordis plugin that registers the bench__work tool with dsh's tool registry: it writes its call id to
// $BENCH_TOOL_LOG (when set), then takes $BENCH_TOOL_MS (default 1000) milliseconds and answers "ok".
import fs from "node:fs";
import { defineTool } from "@deepseek-ai/dsh-tools";

export const name = "bench-work";
export const inject = ["tools"];

export function apply(ctx) {
	const ms = Number(process.env.BENCH_TOOL_MS ?? 1000);
	const log = process.env.BENCH_TOOL_LOG;
	ctx.tools.register(
		defineTool({
			name: "bench__work",
			description: "Does one unit of work.",
			parameters: {},
			output: { schema: { type: "string" }, render: (_args, value) => [{ type: "text", text: value }] },
			isConcurrencySafe: () => true,
			async execute(_args, exec) {
				if (log) fs.appendFileSync(log, `${exec.agent?.id ?? "?"}/${exec.callId}\n`);
				await new Promise((resolve) => setTimeout(resolve, ms));
				return "ok";
			},
		}),
	);
}
