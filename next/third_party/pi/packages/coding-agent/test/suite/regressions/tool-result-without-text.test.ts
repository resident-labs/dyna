/**
 * Dyna's MCP server dropped the `text` field of an empty text block, so a tool that printed nothing
 * left `{"type":"text"}` in the transcript. Token estimation read `block.text.length` before every
 * provider request. The error reply carries no usage, so the block stayed inside the estimated part
 * of the transcript and every later request of the session failed the same way.
 * Upstream report: https://github.com/earendil-works/pi/issues/6819
 */

import { mkdtempSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fauxAssistantMessage } from "@earendil-works/pi-ai";
import { convertResponsesMessages } from "@earendil-works/pi-ai/api/openai-responses-shared";
import { buildBaseOptions } from "@earendil-works/pi-ai/api/simple-options";
import {
	type Api,
	type AssistantMessage,
	type FauxResponseFactory,
	type Message,
	type Model,
	streamSimple,
	type ToolResultMessage,
	type Usage,
} from "@earendil-works/pi-ai/compat";
import { afterEach, describe, expect, it, vi } from "vitest";
import { SessionManager } from "../../../src/core/session-manager.ts";
import { createHarness, type Harness } from "../harness.ts";

/** The failure the session recorded. A request that fails with this text has not recovered. */
const FAILURE = "Cannot read properties of undefined (reading 'length')";

function createUsage(totalTokens: number): Usage {
	return {
		input: totalTokens,
		output: 0,
		cacheRead: 0,
		cacheWrite: 0,
		totalTokens,
		cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, total: 0 },
	};
}

/** A session after one failed turn: a `computer` click, its empty result, and the failed reply. */
function brokenSession(source: Pick<Model<Api>, "api" | "provider" | "id">): Message[] {
	const owner = { api: source.api, provider: source.provider, model: source.id };
	const call: AssistantMessage = {
		role: "assistant",
		content: [{ type: "toolCall", id: "call_1", name: "mcp__dyna__computer", arguments: { action: "click" } }],
		...owner,
		usage: createUsage(1_000),
		stopReason: "toolUse",
		timestamp: 2,
	};
	// Parsed from JSON, as a session file gives it back. The types do not allow this block.
	const result: ToolResultMessage = JSON.parse(
		'{"role":"toolResult","toolCallId":"call_1","toolName":"mcp__dyna__computer","content":[{"type":"text"}],"isError":false,"timestamp":3}',
	);
	const failed: AssistantMessage = {
		role: "assistant",
		content: [],
		...owner,
		usage: createUsage(0),
		stopReason: "error",
		errorMessage: FAILURE,
		timestamp: 4,
	};
	return [{ role: "user", content: "open the page", timestamp: 1 }, call, result, failed];
}

function createModel(api: Api, provider: string): Model<Api> {
	return {
		id: "gpt-6.1-terra",
		name: "test model",
		api,
		provider,
		baseUrl: "http://127.0.0.1:1/v1",
		reasoning: false,
		input: ["text", "image"],
		cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
		contextWindow: 100_000,
		maxTokens: 8_000,
	};
}

/** The token shape the Codex route reads its account from. */
const CODEX_TOKEN = `a.${Buffer.from(JSON.stringify({ "https://api.openai.com/auth": { chatgpt_account_id: "acc" } })).toString("base64")}.b`;

describe("tool results without text", () => {
	const harnesses: Harness[] = [];
	const directories: string[] = [];

	afterEach(() => {
		vi.unstubAllGlobals();
		while (harnesses.length > 0) harnesses.pop()?.cleanup();
		while (directories.length > 0) rmSync(directories.pop() as string, { recursive: true, force: true });
	});

	it.each([
		{ api: "openai-responses", provider: "openai", sent: '"output":"(no tool output)"' },
		{ api: "openai-codex-responses", provider: "openai-codex", sent: '"output":"(no tool output)"' },
		{ api: "openai-completions", provider: "openai", sent: '"role":"tool"' },
		{ api: "anthropic-messages", provider: "anthropic", sent: '"type":"tool_result"' },
		{ api: "google-generative-ai", provider: "google", sent: '"functionResponse"' },
		{ api: "mistral-conversations", provider: "mistral", sent: '"role":"tool"' },
	])("sends the next $api request of a session that holds one", async ({ api, provider, sent }) => {
		vi.stubGlobal("fetch", async () => new Response("stop after the payload", { status: 400 }));
		const model = createModel(api, provider);
		let payload: unknown;
		const messages: Message[] = [...brokenSession(model), { role: "user", content: "try again", timestamp: 5 }];

		const response = await streamSimple(
			model,
			{ messages },
			{
				apiKey: api === "openai-codex-responses" ? CODEX_TOKEN : "test-key",
				maxRetries: 0,
				transport: "sse",
				onPayload: (body) => {
					payload = body;
					return undefined;
				},
			},
		).result();

		expect(response.errorMessage).not.toBe(FAILURE);
		expect(JSON.stringify(payload)).toContain(sent);
	});

	/** The faux provider skips request building, so this runs the real steps on the context it gets. */
	function realRequest(sent: string[]): FauxResponseFactory {
		return (context, _options, _state, model) => {
			buildBaseOptions(model, context);
			const input = convertResponsesMessages({ ...model, api: "openai-responses" }, context, new Set());
			for (const item of input) {
				if ("type" in item && item.type === "function_call_output") sent.push(item.output as string);
			}
			return fauxAssistantMessage("recovered");
		};
	}

	function saveBrokenSession(): SessionManager {
		const directory = mkdtempSync(join(tmpdir(), "pi-without-text-"));
		directories.push(directory);
		const manager = SessionManager.create(directory, directory);
		for (const message of brokenSession({ api: "faux", provider: "faux", id: "faux-1" })) {
			manager.appendMessage(message);
		}
		const file = manager.getSessionFile();
		if (!file) throw new Error("Expected a session file");
		expect(readFileSync(file, "utf8")).toContain('"content":[{"type":"text"}]');
		return SessionManager.open(file);
	}

	it("recovers a saved session on its next prompt", async () => {
		const harness = await createHarness({ sessionManager: saveBrokenSession() });
		harnesses.push(harness);
		const sent: string[] = [];
		harness.setResponses([realRequest(sent)]);

		await harness.session.prompt("try again");

		expect(harness.session.messages.at(-1)).toMatchObject({ role: "assistant", stopReason: "stop" });
		expect(sent).toEqual(["(no tool output)"]);
	});

	it("compacts a saved session that holds one", async () => {
		const harness = await createHarness({
			sessionManager: saveBrokenSession(),
			settings: { compaction: { keepRecentTokens: 1 } },
		});
		harnesses.push(harness);
		harness.setResponses([fauxAssistantMessage("summary")]);

		const result = await harness.session.compact();

		expect(result.summary).toContain("summary");
		expect(harness.session.messages.some((message) => message.role === "compactionSummary")).toBe(true);
	});
});
