import { mkdirSync, mkdtempSync, realpathSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { Args } from "../src/cli/args.ts";
import { SessionManager } from "../src/core/session-manager.ts";
import { SettingsManager } from "../src/core/settings-manager.ts";
import { createSessionManager } from "../src/main.ts";

const tempDirs: string[] = [];

afterEach(() => {
	vi.restoreAllMocks();
	for (const dir of tempDirs.splice(0)) {
		rmSync(dir, { recursive: true, force: true });
	}
});

function createTempDir(): string {
	const dir = realpathSync(mkdtempSync(join(tmpdir(), "pi-headless-session-")));
	tempDirs.push(dir);
	return dir;
}

function args(session: string): Args {
	return {
		messages: [],
		fileArgs: [],
		unknownFlags: new Map(),
		diagnostics: [],
		session,
	};
}

describe("headless --session", () => {
	it("opens a global id without forking and rejects a missing id", async () => {
		const tempRoot = createTempDir();
		const sourceCwd = join(tempRoot, "source");
		const otherCwd = join(tempRoot, "other");
		const sessionDir = join(tempRoot, "sessions");
		mkdirSync(sourceCwd, { recursive: true });
		mkdirSync(otherCwd, { recursive: true });
		const source = SessionManager.create(sourceCwd, sessionDir, { id: "existing-id" });
		source.appendMessage({ role: "user", content: "persist this session", timestamp: Date.now() });

		const opened = await createSessionManager(
			args("existing-id"),
			otherCwd,
			sessionDir,
			SettingsManager.inMemory(),
			"rpc",
		);

		expect(opened.getSessionFile()).toBe(source.getSessionFile());
		expect(opened.getCwd()).toBe(sourceCwd);

		const consoleError = vi.spyOn(console, "error").mockImplementation(() => {});
		vi.spyOn(process, "exit").mockImplementation((code) => {
			throw new Error(`exit:${code}`);
		});
		await expect(
			createSessionManager(args("missing-id"), otherCwd, sessionDir, SettingsManager.inMemory(), "rpc"),
		).rejects.toThrow("exit:2");
		expect(consoleError).toHaveBeenCalledOnce();
		expect(consoleError).toHaveBeenCalledWith("session not found: missing-id");
	});
});
