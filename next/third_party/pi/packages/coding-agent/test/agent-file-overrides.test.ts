import { mkdirSync, rmSync, writeFileSync } from "fs";
import { join } from "path";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { getAgentFileOverrides, getModelsPath, setAgentFileOverrides } from "../src/config.ts";
import { parseArgs } from "../src/cli/args.ts";
import { SettingsManager } from "../src/core/settings-manager.ts";
import { loadMcpConfig } from "../src/extensions/mcp/config.ts";

describe("per-process agent file overrides", () => {
	const testDir = join(process.cwd(), "test-agent-file-overrides-tmp");
	const agentDir = join(testDir, "agent");
	const cwd = join(testDir, "project");

	beforeEach(() => {
		rmSync(testDir, { recursive: true, force: true });
		mkdirSync(agentDir, { recursive: true });
		mkdirSync(cwd, { recursive: true });
	});

	afterEach(() => {
		rmSync(testDir, { recursive: true, force: true });
		setAgentFileOverrides({});
	});

	it("parses the three file flags", () => {
		const parsed = parseArgs([
			"--settings",
			"/tmp/one.json",
			"--mcp-config",
			"/tmp/two.json",
			"--models-file",
			"/tmp/three.json",
		]);
		expect(parsed.settings).toBe("/tmp/one.json");
		expect(parsed.mcpConfig).toBe("/tmp/two.json");
		expect(parsed.modelsFile).toBe("/tmp/three.json");
		expect(parsed.diagnostics).toHaveLength(0);
	});

	it("replaces the user settings file for this process", () => {
		const override = join(testDir, "settings.json");
		writeFileSync(override, JSON.stringify({ defaultProvider: "override-provider" }));
		writeFileSync(join(agentDir, "settings.json"), JSON.stringify({ defaultProvider: "agent-provider" }));
		setAgentFileOverrides({ settings: override });
		const manager = SettingsManager.create(cwd, agentDir);
		expect(manager.getSettings().defaultProvider).toBe("override-provider");
	});

	it("replaces the global mcp.json for this process", () => {
		const override = join(testDir, "mcp.json");
		writeFileSync(override, JSON.stringify({ mcpServers: { overrideServer: { command: "true" } } }));
		writeFileSync(join(agentDir, "mcp.json"), JSON.stringify({ mcpServers: { agentServer: { command: "true" } } }));
		setAgentFileOverrides({ mcpConfig: override });
		const loaded = loadMcpConfig({ agentDir, cwd, projectTrusted: false });
		expect(loaded.servers.map((server) => server.name)).toEqual(["overrideServer"]);
	});

	it("replaces the models file path for this process", () => {
		const override = join(testDir, "models.json");
		setAgentFileOverrides({ models: override });
		expect(getModelsPath()).toBe(override);
		expect(getAgentFileOverrides().models).toBe(override);
	});
});
