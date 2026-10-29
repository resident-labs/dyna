import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import type { Model, OAuthAuth, OAuthCredential, Provider } from "@earendil-works/pi-ai";
import { afterEach, describe, expect, it, vi } from "vitest";
import { parseAuthCommand } from "../src/cli/auth-command.ts";
import { type AuthLoginEvent, runAuthLogin, runAuthRefresh } from "../src/cli/auth-login.ts";
import { ENV_AGENT_DIR } from "../src/config.ts";
import { AuthStorage } from "../src/core/auth-storage.ts";
import { ModelRuntime } from "../src/core/model-runtime.ts";
import { main } from "../src/main.ts";

function fakeModel(): Model<"openai-completions"> {
	return {
		id: "fake-model",
		name: "Fake model",
		api: "openai-completions",
		provider: "fake-oauth",
		baseUrl: "https://example.test/v1",
		reasoning: false,
		input: ["text"],
		cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
		contextWindow: 1000,
		maxTokens: 100,
	};
}

function fakeProvider(oauth: OAuthAuth): Provider<"openai-completions"> {
	return {
		id: "fake-oauth",
		name: "Fake OAuth",
		auth: { oauth },
		getModels: () => [fakeModel()],
		stream: () => {
			throw new Error("unused");
		},
		streamSimple: () => {
			throw new Error("unused");
		},
	};
}

async function fakeRuntime(oauth: OAuthAuth, credential?: OAuthCredential): Promise<ModelRuntime> {
	const credentials = credential ? AuthStorage.inMemory({ "fake-oauth": credential }) : AuthStorage.inMemory();
	const runtime = await ModelRuntime.create({
		credentials,
		modelsPath: null,
		allowModelNetwork: false,
		refreshOnCreate: false,
	});
	runtime.registerNativeProvider(fakeProvider(oauth));
	return runtime;
}

function oauth(overrides: Partial<OAuthAuth> = {}): OAuthAuth {
	return {
		name: "Fake OAuth",
		login: async () => ({ type: "oauth", access: "access", refresh: "refresh", expires: Date.now() + 60_000 }),
		refresh: async (credential) => ({ ...credential, access: "fresh-access" }),
		toAuth: async (credential) => ({ apiKey: credential.access }),
		...overrides,
	};
}

const expired: OAuthCredential = { type: "oauth", access: "old-access", refresh: "refresh", expires: 0 };

afterEach(() => {
	vi.restoreAllMocks();
});

describe("native auth login command", () => {
	it("emits a fake provider's URL and prompt, then done without printing its credential", async () => {
		const runtime = await fakeRuntime(
			oauth({
				login: async (interaction) => {
					interaction.notify({
						type: "auth_url",
						url: "https://example.test/authorize",
						instructions: "Open it.",
					});
					const answer = await interaction.prompt({ type: "manual_code", message: "Paste the code." });
					expect(answer).toBe("fake-code");
					return {
						type: "oauth",
						access: "secret-access",
						refresh: "secret-refresh",
						expires: Date.now() + 60_000,
					};
				},
			}),
		);
		const events: AuthLoginEvent[] = [];

		await expect(
			runAuthLogin(
				runtime,
				"fake-oauth",
				async () => "fake-code",
				(event) => events.push(event),
			),
		).resolves.toBe(true);

		expect(events).toEqual([
			{ type: "auth_url", url: "https://example.test/authorize", instructions: "Open it." },
			{ type: "prompt", message: "Paste the code." },
			{ type: "done" },
		]);
		expect(JSON.stringify(events)).not.toContain("secret-access");
		expect(JSON.stringify(events)).not.toContain("secret-refresh");
	});

	it("parses the headless login and refresh arguments", () => {
		expect(parseAuthCommand(["auth", "login", "--json"])).toEqual({
			kind: "login",
			args: [],
			json: true,
			credentials: false,
			noRefresh: false,
		});
		expect(parseAuthCommand(["auth", "refresh", "--provider", "fake-oauth", "--json"])).toEqual({
			kind: "refresh",
			args: [],
			json: true,
			credentials: false,
			noRefresh: false,
			provider: "fake-oauth",
		});
	});
});

describe("native auth refresh command", () => {
	it("reports no credential and a renewed grant", async () => {
		await expect(runAuthRefresh(await fakeRuntime(oauth()), "fake-oauth")).resolves.toEqual({
			status: "no_credential",
		});
		await expect(runAuthRefresh(await fakeRuntime(oauth(), expired), "fake-oauth")).resolves.toEqual({
			status: "renewed",
		});
	});

	for (const tc of [
		{
			name: "invalid grant",
			failure: Object.assign(new Error('token response: {"error":"invalid_grant"}'), { status: 400 }),
			result: { status: "failed", kind: "invalid_grant", httpStatus: 400 },
		},
		{
			name: "network",
			failure: Object.assign(new Error("fetch failed"), { code: "ENOTFOUND" }),
			result: { status: "failed", kind: "network", httpStatus: 0 },
		},
		{
			name: "timeout",
			failure: Object.assign(new Error("request timed out"), { code: "ETIMEDOUT" }),
			result: { status: "failed", kind: "timeout", httpStatus: 0 },
		},
		{
			name: "unknown",
			failure: Object.assign(new Error("provider failure"), { status: 500 }),
			result: { status: "failed", kind: "unknown", httpStatus: 500 },
		},
	]) {
		it(`reports ${tc.name} as typed output`, async () => {
			const runtime = await fakeRuntime(
				oauth({
					refresh: async () => {
						throw new Error("OAuth refresh failed", { cause: tc.failure });
					},
				}),
				expired,
			);
			await expect(runAuthRefresh(runtime, "fake-oauth")).resolves.toEqual(tc.result);
		});
	}

	it("routes refresh through the CLI entry point in an empty agent dir", async () => {
		const dir = mkdtempSync(join(tmpdir(), "pi-auth-refresh-entry-"));
		const previousAgentDir = process.env[ENV_AGENT_DIR];
		const previousExitCode = process.exitCode;
		process.env[ENV_AGENT_DIR] = dir;
		const output: string[] = [];
		vi.spyOn(console, "log").mockImplementation((line) => output.push(String(line)));
		try {
			await main(["auth", "refresh", "--provider", "openai-codex", "--json"]);
			expect(output).toEqual([JSON.stringify({ status: "no_credential" })]);
			expect(process.exitCode).toBe(3);
		} finally {
			process.exitCode = previousExitCode;
			if (previousAgentDir === undefined) delete process.env[ENV_AGENT_DIR];
			else process.env[ENV_AGENT_DIR] = previousAgentDir;
			rmSync(dir, { recursive: true, force: true });
		}
	});
});
