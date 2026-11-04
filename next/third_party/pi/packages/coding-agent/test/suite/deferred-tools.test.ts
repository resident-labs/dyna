import {
	fauxAssistantMessage,
	fauxToolCall,
	getInitialSystemMessage,
	getSystemMessageText,
	resolveTranscriptTools,
	type SystemMessage,
	type TranscriptContext,
} from "@earendil-works/pi-ai";
import type { TSchema } from "typebox";
import { afterEach, describe, expect, it } from "vitest";
import type { ExtensionFactory } from "../../src/core/extensions/types.ts";
import { MAX_DEFERRED_NAMES, renderDeferredToolsSection } from "../../src/core/system-prompt.ts";
import { createToolSearchExtension } from "../../src/extensions/tool-search/index.ts";
import { createHarness, type Harness } from "./harness.ts";

const schema = (actions?: string[]): TSchema =>
	({
		type: "object",
		properties: actions ? { action: { type: "string", enum: actions } } : { query: { type: "string" } },
	}) as unknown as TSchema;

/** Registers deferred tools in the `mcp__dyna` and `mcp__gh` namespaces, and one direct tool. */
const deferredTools: ExtensionFactory = (pi) => {
	const register = (namespace: string, name: string, description: string, actions?: string[]) =>
		pi.registerTool({
			name: `${namespace}__${name}`,
			label: name,
			description,
			parameters: schema(actions),
			exposure: "deferred",
			namespace: { name: namespace },
			execute: async () => ({ content: [{ type: "text", text: `${name} ran` }], details: {} }),
		});
	register("mcp__dyna", "session", "Start and list sessions.", ["start", "list", "close"]);
	register("mcp__dyna", "goal", "Set goals.", ["set", "done"]);
	register("mcp__gh", "get_me", "Who am I on GitHub.");
	register("mcp__gh", "search_code", "Search code on GitHub.");
	pi.registerTool({
		name: "mcp__dyna__post",
		label: "post",
		description: "Post.",
		parameters: schema(["send"]),
		namespace: { name: "mcp__dyna" },
		execute: async () => ({ content: [{ type: "text", text: "posted" }], details: {} }),
	});
};

const compactWithSummary: ExtensionFactory = (pi) => {
	pi.on("session_before_compact", async (event) => ({
		compaction: {
			summary: "summary",
			firstKeptEntryId: event.preparation.firstKeptEntryId,
			tokensBefore: event.preparation.tokensBefore,
		},
	}));
};

describe("renderDeferredToolsSection", () => {
	const reach = { toolSearch: true, codemode: false };

	it("names tools by namespace without descriptions, with a tool's actions on its own line", () => {
		expect(
			renderDeferredToolsSection(
				[
					{ name: "mcp__gh__search_code", namespace: "mcp__gh", actions: [] },
					{ name: "mcp__dyna__session", namespace: "mcp__dyna", actions: ["start", "list", "close"] },
					{ name: "mcp__gh__get_me", namespace: "mcp__gh", actions: [] },
					{ name: "mcp__dyna__goal", namespace: "mcp__dyna", actions: ["set", "done"] },
					{ name: "loose", actions: [] },
				],
				reach,
			),
		).toBe(
			[
				"Tools not declared to you yet, by name prefix, with a tool's actions after it. tool_search loads one.",
				"loose",
				"mcp__dyna__:",
				"- goal: set, done",
				"- session: start, list, close",
				"mcp__gh__: get_me, search_code",
			].join("\n"),
		);
	});

	it("says how scripts reach the tools when codemode is active", () => {
		const tools = [{ name: "mcp__gh__get_me", namespace: "mcp__gh", actions: [] }];
		expect(renderDeferredToolsSection(tools, { toolSearch: true, codemode: true })?.split("\n")[0]).toBe(
			"Tools not declared to you yet, by name prefix, with a tool's actions after it. tool_search loads one, and a codemode script calls one as tools.<prefix><name>.",
		);
		expect(renderDeferredToolsSection(tools, { toolSearch: false, codemode: true })?.split("\n")[0]).toBe(
			"Tools not declared to you yet, by name prefix, with a tool's actions after it. A codemode script calls one as tools.<prefix><name>.",
		);
		expect(renderDeferredToolsSection(tools, { toolSearch: false, codemode: false })).toBeUndefined();
	});

	it("has no section with nothing deferred", () => {
		expect(renderDeferredToolsSection([], reach)).toBeUndefined();
	});

	it("bounds a namespace with hundreds of tools and a tool with hundreds of actions", () => {
		const many = Array.from({ length: 300 }, (_, index) => ({
			name: `mcp__big__tool_${String(index).padStart(3, "0")}`,
			namespace: "mcp__big",
			actions: [],
		}));
		const actions = Array.from({ length: 100 }, (_, index) => `a${index}`);
		const section =
			renderDeferredToolsSection([...many, { name: "mcp__wide__op", namespace: "mcp__wide", actions }], reach) ?? "";
		const [, big, wideHead, wide] = section.split("\n");
		expect(big.split(", ")).toHaveLength(MAX_DEFERRED_NAMES + 1);
		expect(big).toMatch(/^mcp__big__: tool_000, tool_001, .*, tool_079, \+220 more; tool_search finds them$/);
		expect(wideHead).toBe("mcp__wide__:");
		expect(wide).toMatch(/^- op: a0, a1, .*, a79, \+20 more; tool_search finds them$/);
	});
});

describe("AgentSession deferred tools section", () => {
	const harnesses: Harness[] = [];
	afterEach(() => {
		while (harnesses.length > 0) harnesses.pop()?.cleanup();
	});

	async function setup(extensionFactories: ExtensionFactory[] = [deferredTools]) {
		const harness = await createHarness({
			initialActiveToolNames: ["read", "tool_search"],
			settings: { compaction: { keepRecentTokens: 1 } },
			extensionFactories: [...extensionFactories, createToolSearchExtension(), compactWithSummary],
		});
		harnesses.push(harness);
		return harness;
	}

	/** The `deferred_tools` section of the current head, without its tags. */
	function block(harness: Harness): string | undefined {
		const head = harness.session.messages.find((message): message is SystemMessage => message.role === "system");
		return head?.sections?.deferred_tools?.replace(/^<deferred_tools>\n|\n<\/deferred_tools>$/g, "");
	}

	it("lists the deferred tools by name only, not declared or direct ones", async () => {
		const harness = await setup();
		harness.setResponses([fauxAssistantMessage("ok")]);
		await harness.session.prompt("go");

		expect(block(harness)?.split("\n").slice(1)).toEqual([
			"mcp__dyna__:",
			"- goal: set, done",
			"- session: start, list, close",
			"mcp__gh__: get_me, search_code",
		]);
		expect(block(harness)).not.toContain("Search code on GitHub");
		expect(block(harness)).not.toContain("post");
	});

	it("has no section when nothing is deferred", async () => {
		const harness = await setup([]);
		harness.setResponses([fauxAssistantMessage("ok")]);
		await harness.session.prompt("go");

		const head = harness.session.messages.find((message): message is SystemMessage => message.role === "system");
		expect(head?.sections).toBeDefined();
		expect(head?.sections?.deferred_tools).toBeUndefined();
	});

	it("leaves the system prompt and tools byte-identical when tool_search loads a tool, adding only toolsAdded", async () => {
		const harness = await setup();
		const contexts: TranscriptContext[] = [];
		const capture = (message: ReturnType<typeof fauxAssistantMessage>) => (context: TranscriptContext) => {
			contexts.push(structuredClone(context));
			return message;
		};
		harness.setResponses([
			capture(fauxAssistantMessage("first")),
			capture(
				fauxAssistantMessage([fauxToolCall("tool_search", { query: "search code", limit: 1 })], {
					stopReason: "toolUse",
				}),
			),
			capture(fauxAssistantMessage("loaded")),
		]);
		await harness.session.prompt("one");
		await harness.session.prompt("two");
		expect(harness.session.getActiveToolNames()).toContain("mcp__gh__search_code");

		const [before, , after] = contexts;
		const system = (context: TranscriptContext) => {
			const head = getInitialSystemMessage(context.messages);
			if (!head) throw new Error("no head");
			return getSystemMessageText(head);
		};
		const requestTools = (context: TranscriptContext) =>
			JSON.stringify(resolveTranscriptTools(context.messages, "additions").requestTools);
		expect(system(after)).toBe(system(before));
		expect(system(after)).toContain("mcp__gh__: get_me, search_code");
		expect(requestTools(after)).toBe(requestTools(before));
		expect(JSON.stringify(after.messages.slice(0, before.messages.length))).toBe(JSON.stringify(before.messages));

		// What the load added: the turn's messages and one system message that only declares the tool.
		const added = after.messages.slice(before.messages.length);
		expect(added.filter((message) => message.role !== "system").map((message) => message.role)).toEqual([
			"assistant",
			"user",
			"assistant",
			"toolResult",
		]);
		const systemAdded = added.filter((message): message is SystemMessage => message.role === "system");
		expect(systemAdded).toHaveLength(1);
		expect(systemAdded[0].sections).toBeUndefined();
		expect(systemAdded[0].content).toBe("");
		expect(systemAdded[0].toolsAdded?.map((tool) => tool.name)).toEqual(["mcp__gh__search_code"]);
		expect(systemAdded[0].toolsRemoved).toBeUndefined();
		expect(JSON.stringify(after)).not.toContain("[Context update]");
	});

	it("drops a loaded tool from the section at compaction, when the head is rebuilt", async () => {
		const harness = await setup();
		harness.setResponses([
			fauxAssistantMessage([fauxToolCall("tool_search", { query: "search code", limit: 1 })], {
				stopReason: "toolUse",
			}),
			fauxAssistantMessage("loaded"),
			fauxAssistantMessage("more"),
			fauxAssistantMessage("after"),
		]);
		await harness.session.prompt("one");
		await harness.session.prompt("more");
		expect(block(harness)).toContain("mcp__gh__: get_me, search_code");

		await harness.session.compact();
		expect(block(harness)?.split("\n").at(-1)).toBe("mcp__gh__: get_me");
		await harness.session.prompt("two");
		expect(block(harness)?.split("\n").at(-1)).toBe("mcp__gh__: get_me");
		expect(JSON.stringify(harness.session.messages)).not.toContain("[Context update]");
	});
});
