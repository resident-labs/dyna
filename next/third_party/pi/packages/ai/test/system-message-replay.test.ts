import { Type } from "typebox";
import { describe, expect, test } from "vitest";
import type { Message, Tool, TranscriptContext } from "../src/types.ts";
import { getSystemMessageText } from "../src/utils/text.ts";
import {
	collapseSystemMessages,
	declarationsEqual,
	getCurrentSystemMessage,
	getCurrentSystemPrompt,
	getToolStateChanges,
	hasNonAdditiveToolChanges,
	hasToolRedefinitions,
	normalizeContext,
} from "../src/utils/transcript.ts";

function tool(name: string, description = `${name} tool`): Tool {
	return { name, description, parameters: Type.Object({}) };
}

const transcript = normalizeContext({
	messages: [
		{
			role: "system",
			content: "base",
			sections: { a: "<a>1</a>", b: "<b>1</b>" },
			toolsAdded: [tool("first")],
			timestamp: 10,
		},
		{ role: "user", content: "hello", timestamp: 11 },
		{ role: "system", content: "also do this", timestamp: 12 },
		{ role: "assistant", content: [{ type: "text", text: "ok" }], timestamp: 13 } as never,
		{
			role: "system",
			content: "",
			sections: { a: "<a>2</a>", b: null, c: "<c>1</c>" },
			toolsRemoved: [{ name: "first" }],
			toolsAdded: [tool("second")],
			timestamp: 14,
		},
	],
});

describe("system message and tool state", () => {
	test("keeps the leading prompt while resolving tool changes", () => {
		const current = getCurrentSystemMessage(transcript.messages);
		expect(current).toEqual({
			role: "system",
			content: "base",
			sections: { a: "<a>1</a>", b: "<b>1</b>" },
			toolsAdded: [tool("second")],
			timestamp: 10,
		});
		expect(getCurrentSystemPrompt(transcript.messages)).toBe("base\n\n<a>1</a>\n\n<b>1</b>");
	});

	test("collapse keeps only non-system messages after the replayed head", () => {
		const collapsed = collapseSystemMessages(transcript);
		expect(collapsed.messages.map((message) => message.role)).toEqual(["system", "user", "assistant"]);
		expect(collapseSystemMessages(collapsed)).toEqual(collapsed);
	});

	test("replay of a transcript without system messages is empty", () => {
		const context = normalizeContext({ messages: [{ role: "user", content: "hi", timestamp: 1 }] });
		expect(getCurrentSystemMessage(context.messages)).toBeUndefined();
		expect(getCurrentSystemPrompt(context.messages)).toBe("");
		expect(collapseSystemMessages(context).messages).toEqual(context.messages);
	});

	test("a late system message cannot patch the prompt", () => {
		const context: TranscriptContext = normalizeContext({
			messages: [
				{ role: "user", content: "old session", timestamp: 1 },
				{
					role: "system",
					content: "",
					sections: { preamble: "You are pi." },
					toolsAdded: [tool("x")],
					timestamp: 2,
				},
			],
		});
		expect(getCurrentSystemPrompt(context.messages)).toBe("");
		expect(collapseSystemMessages(context).messages[0]).toMatchObject({ role: "system", toolsAdded: [tool("x")] });
	});

	test("renders the leading prompt", () => {
		const leading = transcript.messages[0];
		if (leading?.role !== "system") throw new Error("expected system message");
		expect(getSystemMessageText(leading)).toBe("base\n\n<a>1</a>\n\n<b>1</b>");
	});

	test("normalizes the legacy prompt and tool fields into a leading system message", () => {
		const messages: Message[] = [{ role: "user", content: "hi", timestamp: 1 }];
		expect(normalizeContext({ messages })).toEqual({ messages });
		expect(normalizeContext({ systemPrompt: "", tools: [], messages })).toEqual({ messages });
		expect(normalizeContext({ systemPrompt: "be brief", tools: [tool("a")], messages }).messages).toEqual([
			{ role: "system", content: "be brief", toolsAdded: [tool("a")], timestamp: 0 },
			...messages,
		]);
	});

	test("compares tool declarations without executable or undefined fields", () => {
		const executable = { ...tool("a"), constrainedSampling: undefined, execute: () => {} } as Tool;
		expect(declarationsEqual(executable, tool("a"))).toBe(true);
		expect(declarationsEqual(tool("a"), tool("a", "changed"))).toBe(false);
		expect(declarationsEqual(tool("a"), { ...tool("a"), constrainedSampling: false })).toBe(false);
	});

	test("tool state changes treat changed definitions as removal plus addition", () => {
		const changes = getToolStateChanges([tool("a"), tool("b")], [tool("b", "changed"), tool("c")]);
		expect(changes).toEqual({
			toolsAdded: [tool("b", "changed"), tool("c")],
			toolsRemoved: [{ name: "a" }, { name: "b" }],
		});
		expect(getToolStateChanges([tool("a")], [tool("a")])).toEqual({ toolsAdded: [], toolsRemoved: [] });
	});

	test("detects non-additive tool history and redefinitions", () => {
		expect(hasNonAdditiveToolChanges(transcript.messages)).toBe(true);
		expect(hasToolRedefinitions(transcript.messages)).toBe(false);
		const additive = normalizeContext({
			messages: [
				{ role: "system", content: "", toolsAdded: [tool("a")], timestamp: 1 },
				{ role: "system", content: "", toolsAdded: [tool("b")], timestamp: 2 },
			],
		});
		expect(hasNonAdditiveToolChanges(additive.messages)).toBe(false);
		const redeclared = normalizeContext({
			messages: [
				{ role: "system", content: "", toolsAdded: [tool("a")], timestamp: 1 },
				{ role: "system", content: "", toolsAdded: [tool("a", "changed")], timestamp: 2 },
			],
		});
		expect(hasNonAdditiveToolChanges(redeclared.messages)).toBe(true);
		expect(hasToolRedefinitions(redeclared.messages)).toBe(true);
	});
});
