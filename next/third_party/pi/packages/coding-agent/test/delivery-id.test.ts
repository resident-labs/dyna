import { mkdtempSync, readFileSync, rmSync } from "node:fs";
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

it("persists a batch of delivery IDs before the first model call", async () => {
	const dir = mkdtempSync(join(tmpdir(), "pi-delivery-"));
	const model = getModel("anthropic", "claude-sonnet-4-5")!;
	const response: AssistantMessage = {
		role: "assistant", content: [{ type: "text", text: "done" }], api: "anthropic-messages",
		provider: "anthropic", model: model.id,
		usage: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, totalTokens: 0,
			cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, total: 0 } },
		stopReason: "stop", timestamp: 0,
	};
	let firstModelUsers: number | undefined;
	const agent = new Agent({
		getApiKey: () => "test-key",
		initialState: { model, systemPrompt: "test", tools: [] },
		streamFn: (_model, context) => {
			firstModelUsers ??= context.messages.filter((message) => message.role === "user").length;
			const stream = createAssistantMessageEventStream();
			queueMicrotask(() => {
				stream.push({ type: "start", partial: response });
				stream.push({ type: "done", reason: "stop", message: response });
				stream.end();
			});
			return stream;
		},
	});
	const manager = SessionManager.create(dir);
	const auth = AuthStorage.inMemory();
	await auth.modify("anthropic", async () => ({ type: "api_key", key: "test-key" }));
	const registry = await createModelRegistry(auth, dir);
	const session = new AgentSession({
		agent, sessionManager: manager, settingsManager: SettingsManager.create(dir, dir),
		cwd: dir, modelRuntime: getModelRuntime(registry), resourceLoader: createTestResourceLoader(),
	});
	try {
		await session.prompt("hello", { deliveryId: "42", additionalInputs: [{ message: "second", deliveryId: "43" }] });
		const entries: { type: string; message?: { role: string; deliveryId?: string } }[] =
			readFileSync(manager.getSessionFile()!, "utf8").trim().split("\n").map((line) => JSON.parse(line));
		const users = entries.filter((item) => item.type === "message" && item.message?.role === "user");
		expect(users.map((entry) => entry.message?.deliveryId)).toEqual(["42", "43"]);
		expect(firstModelUsers).toBe(2);
	} finally {
		session.dispose();
		rmSync(dir, { recursive: true, force: true });
	}
});
