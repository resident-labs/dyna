import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { Agent } from "@earendil-works/pi-agent-core";
import { createAssistantMessageEventStream, getModel, type AssistantMessage } from "@earendil-works/pi-ai/compat";
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
		role: "assistant", content: [{ type: "toolCall", id: "call-1", name: "shell", arguments: {} }],
		api: "anthropic-messages", provider: "anthropic", model: model.id,
		usage: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, totalTokens: 0,
			cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, total: 0 } },
		stopReason: "toolUse", timestamp: 0,
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
		agent, sessionManager: manager, settingsManager: SettingsManager.create(dir, dir),
		cwd: dir, modelRuntime: getModelRuntime(registry), resourceLoader: createTestResourceLoader(),
	});
	try {
		await session.continueRun();
		const results = manager.getEntries().filter((entry) => entry.type === "message" && entry.message.role === "toolResult");
		expect(results).toHaveLength(1);
		const result = results[0];
		if (result.type !== "message" || result.message.role !== "toolResult") throw new Error("missing tool result");
		expect(result.message).toMatchObject({
			toolCallId: "call-1", isError: true, content: [{ type: "text", text: "interrupted; state unknown" }],
		});
	} finally {
		session.dispose();
		rmSync(dir, { recursive: true, force: true });
	}
});
