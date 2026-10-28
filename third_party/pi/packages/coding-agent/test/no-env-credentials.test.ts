import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { defaultProviderAuthContext } from "@earendil-works/pi-ai";
import { anthropicProvider } from "@earendil-works/pi-ai/providers/anthropic";
import { afterEach, describe, expect, it, vi } from "vitest";
import { main } from "../src/main.ts";

const fixture = resolve(import.meta.dirname, "../../mcp/test/fixtures/stdio-server.mjs");
const tempDirs: string[] = [];

afterEach(() => {
	vi.unstubAllEnvs();
	vi.restoreAllMocks();
	for (const dir of tempDirs.splice(0)) rmSync(dir, { recursive: true, force: true });
});

describe("PI_NO_ENV_CREDENTIALS", () => {
	it("removes inherited provider credentials before auth resolution and MCP startup", async () => {
		const tempDir = mkdtempSync(join(tmpdir(), "pi-no-env-credentials-"));
		tempDirs.push(tempDir);
		const agentDir = join(tempDir, "agent");
		mkdirSync(agentDir, { recursive: true });
		const observationPath = join(tempDir, "mcp-environment.json");
		const probePath = join(tempDir, "mcp-probe.mjs");
		writeFileSync(
			probePath,
			[
				'import { writeFileSync } from "node:fs";',
				"writeFileSync(process.argv[2], JSON.stringify({ inheritedKeyPresent: process.env.ANTHROPIC_API_KEY !== undefined, inheritedOAuthHostPresent: process.env.KIMI_OAUTH_HOST !== undefined }));",
				"await import(process.argv[3]);",
			].join("\n"),
		);
		writeFileSync(
			join(agentDir, "mcp.json"),
			JSON.stringify({
				mcpServers: {
					probe: { command: process.execPath, args: [probePath, observationPath, fixture] },
				},
			}),
		);
		vi.stubEnv("HOME", tempDir);
		vi.stubEnv("PI_CODING_AGENT_DIR", agentDir);
		vi.stubEnv("PI_OFFLINE", "1");
		vi.stubEnv("PI_SKIP_VERSION_CHECK", "1");
		vi.stubEnv("PI_NO_ENV_CREDENTIALS", "1");
		vi.stubEnv("ANTHROPIC_API_KEY", "inherited-test-key");
		vi.stubEnv("KIMI_OAUTH_HOST", "https://inherited-oauth.example");
		vi.spyOn(console, "log").mockImplementation(() => {});

		const previousExitCode = process.exitCode;
		let startupExitCode: number | undefined;
		try {
			await main(["mcp", "list", "--json"]);
			startupExitCode = process.exitCode;
		} finally {
			process.exitCode = previousExitCode;
		}
		const apiKeyAuth = anthropicProvider().auth.apiKey;
		if (!apiKeyAuth) throw new Error("Anthropic API-key auth is missing");
		const auth = await apiKeyAuth.resolve({
			ctx: defaultProviderAuthContext(),
			signal: new AbortController().signal,
		});

		expect({
			startupExitCode,
			inheritedKey: process.env.ANTHROPIC_API_KEY,
			inheritedOAuthHost: process.env.KIMI_OAUTH_HOST,
			resolvedAuth: auth,
			mcpEnvironment: readFileSync(observationPath, "utf8"),
		}).toEqual({
			startupExitCode: 0,
			inheritedKey: undefined,
			inheritedOAuthHost: undefined,
			resolvedAuth: undefined,
			mcpEnvironment: '{"inheritedKeyPresent":false,"inheritedOAuthHostPresent":false}',
		});
	});
});
