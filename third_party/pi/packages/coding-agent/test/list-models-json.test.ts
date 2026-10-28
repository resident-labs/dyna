import { describe, expect, test } from "vitest";
import type { Model } from "@earendil-works/pi-ai";
import { listModels } from "../src/cli/list-models.ts";
import type { ModelRuntime } from "../src/core/model-runtime.ts";

function runtimeWith(models: Model<any>[]): ModelRuntime {
	return {
		getError: () => undefined,
		getAvailable: async () => models,
	} as unknown as ModelRuntime;
}

describe("listModels --json", () => {
	test("prints one JSON array with exact limits and thinking levels", async () => {
		const runtime = runtimeWith([
			{
				provider: "openai-codex",
				id: "gpt-6.1-terra",
				name: "Terra",
				contextWindow: 483616,
				maxTokens: 128000,
				reasoning: true,
			},
			{
				provider: "anthropic",
				id: "claude-fable-5",
				name: "Claude Fable 5",
				contextWindow: 200000,
				maxTokens: 64000,
				reasoning: false,
			},
		] as unknown as Model<any>[]);
		const lines: string[] = [];
		const original = console.log;
		console.log = (line: string) => lines.push(line);
		try {
			await listModels(runtime, undefined, undefined, true);
		} finally {
			console.log = original;
		}
		expect(lines).toHaveLength(1);
		const entries = JSON.parse(lines[0]);
		expect(Array.isArray(entries)).toBe(true);
		expect(entries).toHaveLength(2);
		expect(entries[0]).toEqual({
			provider: "anthropic",
			id: "claude-fable-5",
			name: "Claude Fable 5",
			contextWindow: 200000,
			maxTokens: 64000,
			reasoning: false,
			thinkingLevels: ["off"],
		});
		expect(entries[1]).toEqual({
			provider: "openai-codex",
			id: "gpt-6.1-terra",
			name: "Terra",
			contextWindow: 483616,
			maxTokens: 128000,
			reasoning: true,
			thinkingLevels: ["off", "minimal", "low", "medium", "high"],
		});
		// A model without a thinkingLevelMap offers the base levels only:
		// xhigh and max exist only where the catalog maps them.
	});

	test("prints an empty array when the search matches nothing", async () => {
		const runtime = runtimeWith([
			{ provider: "anthropic", id: "claude-fable-5", name: "Claude Fable 5", contextWindow: 200000, maxTokens: 64000, reasoning: false },
		] as unknown as Model<any>[]);
		const lines: string[] = [];
		const original = console.log;
		console.log = (line: string) => lines.push(line);
		try {
			await listModels(runtime, "nothing-matches", undefined, true);
		} finally {
			console.log = original;
		}
		expect(lines).toHaveLength(1);
		expect(JSON.parse(lines[0])).toEqual([]);
	});
});
