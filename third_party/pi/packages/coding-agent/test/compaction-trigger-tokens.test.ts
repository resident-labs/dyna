import { existsSync, mkdirSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { describe, expect, test } from "vitest";
import { shouldCompact, withCompactionTriggerOverride } from "../src/core/compaction/compaction.ts";
import { parseArgs } from "../src/cli/args.ts";
import { SettingsManager } from "../src/core/settings-manager.ts";

// A model that runs on derived metadata can be absent from every model list;
// the global compaction.triggerTokens must still govern it.
describe("compaction.triggerTokens", () => {
	const window = 483616;
	const reserve = 16384;
	const base = { enabled: true, reserveTokens: reserve, keepRecentTokens: 20000 };

	test("an unlisted model with window 483616 compacts above the absolute trigger", () => {
		expect(shouldCompact(200001, window, { ...base, triggerTokens: 200000 })).toBe(true);
		expect(shouldCompact(200000, window, { ...base, triggerTokens: 200000 })).toBe(false);
	});

	test("the reserve limit stays the ceiling", () => {
		const ceiling = window - reserve;
		expect(shouldCompact(ceiling, window, { ...base, triggerTokens: 600000 })).toBe(false);
		expect(shouldCompact(ceiling + 1, window, { ...base, triggerTokens: 600000 })).toBe(true);
	});

	test("unset keeps the reserve-based behavior exactly", () => {
		expect(shouldCompact(200001, window, base)).toBe(false);
		expect(shouldCompact(window - reserve, window, base)).toBe(false);
		expect(shouldCompact(window - reserve + 1, window, base)).toBe(true);
	});

	test("settings resolve triggerTokens through model override, ordinary setting, then unset", () => {
		const dir = join(tmpdir(), `pi-trigger-tokens-${Date.now()}`);
		mkdirSync(dir, { recursive: true });
		try {
			const manager = SettingsManager.create(dir, dir);
			const terra = { provider: "openai-codex", id: "gpt-6.1-terra" };
			expect(manager.getCompactionSettings(terra).triggerTokens).toBeUndefined();
			manager.applyOverrides({ compaction: { triggerTokens: 200000 } });
			expect(manager.getCompactionSettings(terra).triggerTokens).toBe(200000);
			manager.applyOverrides({
				compaction: {
					triggerTokens: 150000,
					modelOverrides: { "openai-codex/gpt-6.1-terra": { triggerTokens: 200000 } },
				},
			});
			expect(manager.getCompactionSettings(terra).triggerTokens).toBe(200000);
			expect(manager.getCompactionSettings({ provider: "openai-codex", id: "gpt-6.1-sol" }).triggerTokens).toBe(150000);
		} finally {
			if (existsSync(dir)) rmSync(dir, { recursive: true, force: true });
		}
	});
});

describe("--compaction-trigger-tokens", () => {
	const window = 483616;
	const reserve = 16384;

	test("parses a non-negative integer and rejects other values", () => {
		expect(parseArgs(["--compaction-trigger-tokens", "150000"]).compactionTriggerTokens).toBe(150000);
		expect(parseArgs(["--compaction-trigger-tokens", "0"]).compactionTriggerTokens).toBe(0);
		for (const bad of ["-1", "1.5", "abc"]) {
			const parsed = parseArgs(["--compaction-trigger-tokens", bad]);
			expect(parsed.compactionTriggerTokens).toBeUndefined();
			expect(parsed.diagnostics.some((d) => d.type === "error")).toBe(true);
		}
	});

	test("wins over the setting and model overrides; unset keeps them", () => {
		const dir = join(tmpdir(), `pi-trigger-flag-${Date.now()}`);
		mkdirSync(dir, { recursive: true });
		try {
			const manager = SettingsManager.create(dir, dir);
			manager.applyOverrides({
				compaction: {
				triggerTokens: 200000,
				modelOverrides: { "openai-codex/gpt-6.1-terra": { triggerTokens: 250000 } },
				},
			});
			const terra = { provider: "openai-codex", id: "gpt-6.1-terra" };
			const resolved = manager.getCompactionSettings(terra);
			expect(resolved.triggerTokens).toBe(250000);

			const flagged = withCompactionTriggerOverride(resolved, 150000);
			expect(shouldCompact(150001, window, flagged)).toBe(true);
			expect(shouldCompact(150000, window, flagged)).toBe(false);
			// Without the flag the model override keeps governing.
			expect(withCompactionTriggerOverride(resolved, undefined)).toEqual(resolved);
		} finally {
			if (existsSync(dir)) rmSync(dir, { recursive: true, force: true });
		}
	});
});
