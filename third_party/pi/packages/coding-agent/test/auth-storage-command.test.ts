import { mkdtempSync, readFileSync, rmSync } from "fs";
import { tmpdir } from "os";
import { join } from "path";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { AuthCommandError, parseAuthCommand, runAuthStorageCommand } from "../src/cli/auth-command.ts";
import { FileAuthStorageBackend } from "../src/core/auth-storage.ts";

function storageCommand(args: string[]) {
	const command = parseAuthCommand(args);
	if (!command) throw new Error("the auth command did not parse");
	return command;
}

describe("auth storage commands", () => {
	let dir: string;
	let backend: FileAuthStorageBackend;
	let output: string[];

	beforeEach(() => {
		dir = mkdtempSync(join(tmpdir(), "pi-auth-storage-"));
		backend = new FileAuthStorageBackend(join(dir, "auth.json"));
		output = [];
		const log = console.log;
		console.log = (line: string) => {
			output.push(String(line));
		};
		afterEach(() => {
			console.log = log;
			rmSync(dir, { recursive: true, force: true });
		});
	});

	it("sets, lists and removes one entry", async () => {
		const entry = { type: "api_key", key: "test-key" };
		await runAuthStorageCommand(
			storageCommand(["auth", "set", "--provider", "alpha"]),
			JSON.stringify(entry),
			backend,
		);
		await runAuthStorageCommand(storageCommand(["auth", "list", "--json"]), undefined, backend);
		expect(JSON.parse(output[0])).toEqual({ alpha: entry });

		await runAuthStorageCommand(storageCommand(["auth", "list"]), undefined, backend);
		expect(output[1]).toBe("alpha");

		await runAuthStorageCommand(storageCommand(["auth", "remove", "--provider", "alpha"]), undefined, backend);
		await runAuthStorageCommand(storageCommand(["auth", "list", "--json"]), undefined, backend);
		expect(JSON.parse(output[2])).toEqual({});

		// A missing entry is not an error.
		await runAuthStorageCommand(storageCommand(["auth", "remove", "--provider", "alpha"]), undefined, backend);
	});

	it("waits for a second writer holding the lock", async () => {
		const holder = backend.withLockAsync(async () => {
			await new Promise((resolve) => {
				setTimeout(resolve, 150);
			});
			return { result: undefined };
		});
		const started = Date.now();
		await runAuthStorageCommand(
			storageCommand(["auth", "set", "--provider", "beta"]),
			JSON.stringify({ type: "api_key", key: "beta-key" }),
			backend,
		);
		const waited = Date.now() - started;
		await holder;

		expect(waited).toBeGreaterThanOrEqual(100);
		const stored = JSON.parse(readFileSync(join(dir, "auth.json"), "utf-8"));
		expect(stored).toEqual({ beta: { type: "api_key", key: "beta-key" } });
	});

	it("rejects a set without a provider or with invalid stdin JSON", async () => {
		expect(() => storageCommand(["auth", "set"])).toThrow(AuthCommandError);
		await expect(
			runAuthStorageCommand(storageCommand(["auth", "set", "--provider", "alpha"]), "{not json", backend),
		).rejects.toThrow(AuthCommandError);
		await expect(
			runAuthStorageCommand(storageCommand(["auth", "set", "--provider", "alpha"]), undefined, backend),
		).rejects.toThrow("stdin");
	});
});
