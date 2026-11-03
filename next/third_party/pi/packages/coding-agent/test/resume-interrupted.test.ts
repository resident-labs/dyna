import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { Agent } from "@earendil-works/pi-agent-core";
import { type AssistantMessage, createAssistantMessageEventStream, getModel } from "@earendil-works/pi-ai/compat";
import { Type } from "typebox";
import { expect, it } from "vitest";
import { AgentSession } from "../src/core/agent-session.ts";
import { AuthStorage } from "../src/core/auth-storage.ts";
import { SessionManager } from "../src/core/session-manager.ts";
import { SettingsManager } from "../src/core/settings-manager.ts";
import { createModelRegistry, getModelRuntime } from "./model-runtime-test-utils.ts";
import { createTestResourceLoader } from "./utilities.ts";

it("continues an interrupted tool call without running it again", async () => {
	const dir = mkdtempSync(join(tmpdir(), "pi-resume-"));
	const model = getModel("anthropic", "claude-sonnet-4-5")!;
	const assistant: AssistantMessage = {
		role: "assistant",
		content: [{ type: "toolCall", id: "call-1", name: "shell", arguments: {} }],
		api: "anthropic-messages",
		provider: "anthropic",
		model: model.id,
		usage: {
			input: 0,
			output: 0,
			cacheRead: 0,
			cacheWrite: 0,
			totalTokens: 0,
			cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, total: 0 },
		},
		stopReason: "toolUse",
		timestamp: 0,
	};
	const answer: AssistantMessage = { ...assistant, content: [{ type: "text", text: "done" }], stopReason: "stop" };
	const manager = SessionManager.inMemory();
	const user = { role: "user" as const, content: "hello", timestamp: 0 };
	manager.appendMessage(user);
	manager.appendMessage(assistant);
	const agent = new Agent({
		getApiKey: () => "test-key",
		initialState: { model, systemPrompt: "test", tools: [], messages: [user, assistant] },
		streamFn: () => {
			const stream = createAssistantMessageEventStream();
			queueMicrotask(() => {
				stream.push({ type: "start", partial: answer });
				stream.push({ type: "done", reason: "stop", message: answer });
				stream.end();
			});
			return stream;
		},
	});
	const auth = AuthStorage.inMemory();
	await auth.modify("anthropic", async () => ({ type: "api_key", key: "test-key" }));
	const registry = await createModelRegistry(auth, dir);
	const session = new AgentSession({
		agent,
		sessionManager: manager,
		settingsManager: SettingsManager.create(dir, dir),
		cwd: dir,
		modelRuntime: getModelRuntime(registry),
		resourceLoader: createTestResourceLoader(),
	});
	try {
		await session.continueRun();
		const results = manager
			.getEntries()
			.filter((entry) => entry.type === "message" && entry.message.role === "toolResult");
		expect(results).toHaveLength(1);
		const result = results[0];
		if (result.type !== "message" || result.message.role !== "toolResult") throw new Error("missing tool result");
		expect(result.message).toMatchObject({
			toolCallId: "call-1",
			isError: true,
			content: [{ type: "text", text: "interrupted: the session restarted while this call ran, so whether it finished is unknown, and it is not run again. A command it started on another machine can still be running there" }],
		});
	} finally {
		session.dispose();
		rmSync(dir, { recursive: true, force: true });
	}
});

async function recoveredSession(last: "error" | "aborted" | "stop", withToolCall = false) {
	const dir = mkdtempSync(join(tmpdir(), "pi-resume-"));
	const model = getModel("anthropic", "claude-sonnet-4-5")!;
	const base: AssistantMessage = {
		role: "assistant",
		content: [],
		api: "anthropic-messages",
		provider: "anthropic",
		model: model.id,
		usage: {
			input: 0,
			output: 0,
			cacheRead: 0,
			cacheWrite: 0,
			totalTokens: 0,
			cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, total: 0 },
		},
		stopReason: "stop",
		timestamp: 0,
	};
	const failed: AssistantMessage = {
		...base,
		stopReason: last,
		...(last === "error" ? { errorMessage: "503 auth_unavailable" } : {}),
		content: withToolCall
			? [
					{ type: "text", text: "partial" },
					{ type: "toolCall", id: "call-1", name: "shell", arguments: {} },
				]
			: last === "stop"
				? [{ type: "text", text: "done" }]
				: [],
	};
	const manager = SessionManager.inMemory();
	const user = { role: "user" as const, content: "hello", timestamp: 0 };
	manager.appendMessage(user);
	manager.appendMessage(failed);
	let calls = 0;
	let toolRuns = 0;
	const agent = new Agent({
		getApiKey: () => "test-key",
		initialState: {
			model,
			systemPrompt: "test",
			messages: [user, failed],
			tools: [
				{
					name: "shell",
					label: "shell",
					description: "shell",
					parameters: Type.Object({}),
					execute: async () => {
						toolRuns++;
						return { content: [{ type: "text", text: "ran" }], details: {} };
					},
				},
			],
		},
		streamFn: () => {
			calls++;
			const answer: AssistantMessage = { ...base, content: [{ type: "text", text: "ok" }] };
			const stream = createAssistantMessageEventStream();
			queueMicrotask(() => {
				stream.push({ type: "start", partial: answer });
				stream.push({ type: "done", reason: "stop", message: answer });
				stream.end();
			});
			return stream;
		},
	});
	const auth = AuthStorage.inMemory();
	await auth.modify("anthropic", async () => ({ type: "api_key", key: "test-key" }));
	const registry = await createModelRegistry(auth, dir);
	const settingsManager = SettingsManager.create(dir, dir);
	const modelRuntime = getModelRuntime(registry);
	const session = new AgentSession({
		agent,
		sessionManager: manager,
		settingsManager,
		cwd: dir,
		modelRuntime,
		resourceLoader: createTestResourceLoader(),
	});
	return {
		session,
		agent,
		manager,
		calls: () => calls,
		toolRuns: () => toolRuns,
		cleanup: () => {
			session.dispose();
			rmSync(dir, { recursive: true, force: true });
		},
	};
}

it("continues after an error reply and saves its omission", async () => {
	const { session, agent, manager, calls, cleanup } = await recoveredSession("error");
	try {
		await session.continueRun();
		expect(calls()).toBe(1);
		expect(agent.state.messages.filter((m) => m.role === "assistant")).toHaveLength(1);
		expect(manager.getEntries().some((entry) => entry.type === "context_edit")).toBe(true);
		const projected = manager.buildSessionProjection().entries.flatMap((entry) => entry.messages);
		expect(projected.some((m) => m.role === "assistant" && m.stopReason === "error")).toBe(false);
	} finally {
		cleanup();
	}
});

it("leaves out an error reply with partial text and a tool call, and never runs the tool", async () => {
	const { session, manager, calls, toolRuns, cleanup } = await recoveredSession("error", true);
	try {
		await session.continueRun();
		expect(calls()).toBe(1);
		expect(toolRuns()).toBe(0);
		const results = manager.getEntries().filter((e) => e.type === "message" && e.message.role === "toolResult");
		expect(results).toHaveLength(0);
	} finally {
		cleanup();
	}
});

it.each(["aborted", "stop"] as const)("refuses a recovered session that ended on a %s reply", async (reason) => {
	const { session, calls, cleanup } = await recoveredSession(reason);
	try {
		await expect(session.continueRun()).rejects.toThrow("no pending input or tool result");
		expect(calls()).toBe(0);
	} finally {
		cleanup();
	}
});
