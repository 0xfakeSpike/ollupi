// pi-durable: one @earendil-works/pi-durable Harness over one SQLite file (openNodeSqliteStorage), hosting many
// ownerless conversations, the shape closest to ollupi's one process over one database. The model is an
// openai-completions provider built with pi-ai's createProvider, pointed at the fake provider.
import fs from "node:fs";
import { BACKGROUND_CONTEXT } from "@earendil-works/chord/context";
import { Type } from "@earendil-works/pi-ai";
import { createModels, createProvider } from "@earendil-works/pi-ai/models";
import { openAICompletionsApi } from "@earendil-works/pi-ai/api/openai-completions.lazy";
import { createRegistry, defineExtension, defineTool, Harness, section } from "@earendil-works/pi-durable";
import { openNodeSqliteStorage } from "@earendil-works/pi-durable/storage/sqlite/node";
import { sleep } from "./common.mjs";

process.env.PI_OFFLINE = "1";
export const ctx = BACKGROUND_CONTEXT;
export const MODEL = { provider: "fake", modelId: "fake" };

export function fakeModels(url) {
	const models = createModels();
	models.setProvider(
		createProvider({
			id: "fake",
			baseUrl: url,
			auth: { apiKey: { name: "dummy", resolve: async () => ({ auth: { apiKey: "dummy" }, source: "bench" }) } },
			models: [
				{
					id: "fake",
					name: "fake",
					api: "openai-completions",
					provider: "fake",
					baseUrl: url,
					input: ["text"],
					reasoning: false,
					contextWindow: 200000,
					maxTokens: 32000,
					cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
					compat: { supportsStore: false, supportsDeveloperRole: false, supportsReasoningEffort: false },
				},
			],
			api: openAICompletionsApi(),
		}),
	);
	return models;
}

// The bench__work tool; replay "unsafe" is pi-durable's default: a call cut off by a crash is not run again.
export function benchExtension({ toolMs = 1000, log, replay = "unsafe" } = {}) {
	const work = defineTool({
		name: "bench__work",
		description: "Does one unit of work.",
		parameters: Type.Object({}),
		replay,
		async execute(_args, api) {
			if (log) fs.appendFileSync(log, `${api.taskId} ${api.conversationId}/${api.callId}\n`);
			await sleep(toolMs);
			return { output: [{ type: "text", text: "ok" }] };
		},
	});
	return defineExtension({
		name: "bench",
		tools: [work],
		sections: [section("preamble", () => "You are a bench assistant. Call bench__work when asked to work.", { tag: false })],
	});
}

export async function openDurable({ file, url, toolMs = 1000, log, settings = {}, onReport }) {
	const registry = createRegistry();
	registry.install(benchExtension({ toolMs, log }));
	const storage = await openNodeSqliteStorage(file);
	const harness = await Harness.open(
		storage,
		{
			models: fakeModels(url),
			registry,
			settings: { compaction: { enabled: false }, ...settings },
			identity: { attribution: null },
			onReport: onReport ?? ((err, info) => console.error("report", info.type, err?.message ?? err)),
		},
		ctx,
	);
	return {
		harness,
		newConversation: () => harness.createConversation({ ownership: { kind: "ownerless" }, agent: { model: MODEL } }, ctx),
		conversation: (id) => harness.conversation(id, ctx),
		close: () => harness.close(ctx),
	};
}

// runTurn submits one input and waits for it to settle.
export async function runTurn(conv, text = "work", requestId) {
	const at = performance.now();
	const sub = await conv.submit({ type: "input", content: text, ...(requestId ? { requestId } : {}) }, ctx);
	const settled = await sub.wait(ctx);
	return { ms: performance.now() - at, failed: settled.status !== "done", reason: settled.reason, id: sub.id };
}
