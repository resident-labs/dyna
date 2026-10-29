import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { defaultProviderAuthContext } from "@earendil-works/pi-ai";
import { amazonBedrockProvider } from "@earendil-works/pi-ai/providers/amazon-bedrock";
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
		const credentialsPath = join(tempDir, "owner-aws-credentials");
		writeFileSync(
			credentialsPath,
			"[stored-profile]\naws_access_key_id = inherited-owner-key\naws_secret_access_key = inherited-owner-secret\n",
		);
		const probePath = join(tempDir, "mcp-probe.mjs");
		writeFileSync(
			probePath,
			[
				'import { writeFileSync } from "node:fs";',
				"writeFileSync(process.argv[2], JSON.stringify({ inheritedKeyPresent: process.env.ANTHROPIC_API_KEY !== undefined, inheritedOAuthHostPresent: process.env.KIMI_OAUTH_HOST !== undefined, inheritedAwsCredentialsFilePresent: process.env.AWS_SHARED_CREDENTIALS_FILE !== undefined, inheritedAwsContainerTokenPresent: process.env.AWS_CONTAINER_AUTHORIZATION_TOKEN !== undefined }));",
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
		vi.stubEnv("AWS_SHARED_CREDENTIALS_FILE", credentialsPath);
		vi.stubEnv("AWS_CONTAINER_AUTHORIZATION_TOKEN", "inherited-container-token");
		vi.spyOn(console, "log").mockImplementation(() => {});

		const previousExitCode = process.exitCode;
		let startupExitCode: typeof process.exitCode;
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
		const bedrockApiKeyAuth = amazonBedrockProvider().auth.apiKey;
		if (!bedrockApiKeyAuth) throw new Error("Bedrock API-key auth is missing");
		const bedrockAuth = await bedrockApiKeyAuth.resolve({
			ctx: defaultProviderAuthContext(),
			credential: { type: "api_key", env: { AWS_PROFILE: "stored-profile" } },
			signal: new AbortController().signal,
		});

		expect({
			startupExitCode,
			inheritedKey: process.env.ANTHROPIC_API_KEY,
			inheritedOAuthHost: process.env.KIMI_OAUTH_HOST,
			inheritedAwsCredentialsFile: process.env.AWS_SHARED_CREDENTIALS_FILE,
			inheritedAwsContainerToken: process.env.AWS_CONTAINER_AUTHORIZATION_TOKEN,
			resolvedAuth: auth,
			resolvedBedrockAuth: bedrockAuth,
			mcpEnvironment: readFileSync(observationPath, "utf8"),
		}).toEqual({
			startupExitCode: 0,
			inheritedKey: undefined,
			inheritedOAuthHost: undefined,
			inheritedAwsCredentialsFile: undefined,
			inheritedAwsContainerToken: undefined,
			resolvedAuth: undefined,
			resolvedBedrockAuth: {
				auth: {},
				env: { AWS_PROFILE: "stored-profile" },
				source: "stored credential",
			},
			mcpEnvironment:
				'{"inheritedKeyPresent":false,"inheritedOAuthHostPresent":false,"inheritedAwsCredentialsFilePresent":false,"inheritedAwsContainerTokenPresent":false}',
		});
	});
});
