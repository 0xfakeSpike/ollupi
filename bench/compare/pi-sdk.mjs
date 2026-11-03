// pi-sdk: plain @earendil-works/pi-coding-agent sessions (AgentSession), many in one process. Each session persists to
// its own JSONL file through SessionManager.create(), as the CLI does; the model is an openai-completions provider in a
// models.json under run/, so the user's ~/.pi is never read or written.
import fs from "node:fs";
import path from "node:path";
import { Type } from "typebox";
import {
	createAgentSession,
	createExtensionRuntime,
	defineTool,
	ModelRuntime,
	SessionManager,
	SettingsManager,
} from "@earendil-works/pi-coding-agent";
import { sleep } from "./common.mjs";

process.env.PI_OFFLINE = "1";

// benchWork is the bench__work tool: it writes its call id to log (when given), then takes toolMs.
export function benchWork({ toolMs = 1000, log } = {}) {
	return defineTool({
		name: "bench__work",
		label: "bench work",
		description: "Does one unit of work.",
		parameters: Type.Object({}),
		async execute(toolCallId, _params, _signal, _onUpdate, ctx) {
			if (log) fs.appendFileSync(log, `${ctx?.sessionManager?.getSessionId?.()}/${toolCallId}\n`);
			await sleep(toolMs);
			return { content: [{ type: "text", text: "ok" }], details: {} };
		},
	});
}

export async function openPi({ dir, url, toolMs = 1000, log, retry }) {
	const agentDir = path.join(dir, "agent");
	fs.mkdirSync(agentDir, { recursive: true });
	fs.writeFileSync(
		path.join(agentDir, "models.json"),
		JSON.stringify({
			providers: {
				fake: {
					baseUrl: url,
					api: "openai-completions",
					apiKey: "dummy",
					compat: { supportsStore: false, supportsDeveloperRole: false, supportsReasoningEffort: false },
					models: [{ id: "fake", contextWindow: 200000, maxTokens: 32000 }],
				},
			},
		}),
	);
	const modelRuntime = await ModelRuntime.create({
		authPath: path.join(agentDir, "auth.json"),
		modelsPath: path.join(agentDir, "models.json"),
		refreshOnCreate: false,
	});
	const model = modelRuntime.getModel("fake", "fake");
	if (!model) throw new Error("fake model not found: " + modelRuntime.getError());
	const settings = { compaction: { enabled: false } };
	if (retry) settings.retry = retry;
	const resourceLoader = {
		getExtensions: () => ({ extensions: [], errors: [], runtime: createExtensionRuntime() }),
		getSkills: () => ({ skills: [], diagnostics: [] }),
		getPrompts: () => ({ prompts: [], diagnostics: [] }),
		getThemes: () => ({ themes: [], diagnostics: [] }),
		getAgentsFiles: () => ({ agentsFiles: [] }),
		getSystemPrompt: () => "You are a bench assistant. Call bench__work when asked to work.",
		getSystemPromptSource: () => undefined,
		getAppendSystemPrompt: () => [],
		getAppendSystemPromptSources: () => [],
		extendResources: () => {},
		reload: async () => {},
	};
	const tool = benchWork({ toolMs, log });
	const cwd = path.join(dir, "cwd");
	const sessionDir = path.join(dir, "sessions");
	fs.mkdirSync(cwd, { recursive: true });
	fs.mkdirSync(sessionDir, { recursive: true });

	async function make(sessionManager) {
		const { session } = await createAgentSession({
			cwd,
			agentDir,
			model,
			thinkingLevel: "off",
			modelRuntime,
			resourceLoader,
			tools: ["bench__work"],
			customTools: [tool],
			sessionManager,
			settingsManager: SettingsManager.inMemory(settings),
		});
		return session;
	}
	return {
		cwd,
		sessionDir,
		newSession: () => make(SessionManager.create(cwd, sessionDir)),
		openSession: (file) => make(SessionManager.open(file, sessionDir)),
		list: () => SessionManager.list(cwd, sessionDir),
	};
}

// runTurn sends one prompt and resolves once pi settles; it reports whether the turn ended with an answer or an error.
export async function runTurn(session, text = "work") {
	const at = performance.now();
	let error;
	try {
		await session.prompt(text);
	} catch (e) {
		error = e;
	}
	const msgs = session.messages;
	const last = msgs[msgs.length - 1];
	const failed = error !== undefined || last?.role !== "assistant" || last?.stopReason === "error" || last?.stopReason === "aborted";
	return { ms: performance.now() - at, failed, error: error?.message ?? last?.errorMessage };
}
