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

	it("applies a whole edit under one lock and prints the entries", async () => {
		await runAuthStorageCommand(
			storageCommand(["auth", "set", "--provider", "alpha"]),
			JSON.stringify({ type: "api_key", key: "a" }),
			backend,
		);
		await runAuthStorageCommand(
			storageCommand(["auth", "apply"]),
			JSON.stringify({ set: { beta: { type: "api_key", key: "b" } }, remove: ["alpha"] }),
			backend,
		);
		await runAuthStorageCommand(storageCommand(["auth", "list", "--json"]), undefined, backend);
		expect(JSON.parse(output[1])).toEqual({ beta: { type: "api_key", key: "b" } });
	});

	it("survives concurrent first use without losing a written login", async () => {
		// Every backend here sees the same missing file at first. An
		// exclusive create keeps any placeholder from replacing a login
		// another backend wrote first, so every written provider survives.
		const runs = [1, 2, 3, 4, 5, 6, 7, 8].map((n) =>
			runAuthStorageCommand(
				storageCommand(["auth", "set", "--provider", `p${n}`]),
				JSON.stringify({ type: "api_key", key: `key-${n}` }),
				new FileAuthStorageBackend(join(dir, "auth.json")),
			),
		);
		await Promise.all(runs);
		const stored = JSON.parse(readFileSync(join(dir, "auth.json"), "utf-8"));
		for (let n = 1; n <= 8; n++) {
			expect(stored[`p${n}`]).toEqual({ type: "api_key", key: `key-${n}` });
		}
	});

	it("reports the replaced entries as before, so a caller can roll back", async () => {
		await runAuthStorageCommand(
			storageCommand(["auth", "set", "--provider", "alpha"]),
			JSON.stringify({ type: "api_key", key: "old" }),
			backend,
		);
		await runAuthStorageCommand(
			storageCommand(["auth", "apply"]),
			JSON.stringify({ set: { alpha: { type: "api_key", key: "new" } } }),
			backend,
		);
		const report = JSON.parse(output[0]);
		expect(report.before).toEqual({ alpha: { type: "api_key", key: "old" } });
		expect(report.after).toEqual({ alpha: { type: "api_key", key: "new" } });
		const stored = JSON.parse(readFileSync(join(dir, "auth.json"), "utf-8"));
		expect(stored).toEqual(report.after);
	});

	it("applies when expect still matches the file", async () => {
		await runAuthStorageCommand(
			storageCommand(["auth", "set", "--provider", "alpha"]),
			JSON.stringify({ type: "api_key", key: "a" }),
			backend,
		);
		await runAuthStorageCommand(
			storageCommand(["auth", "apply"]),
			JSON.stringify({ expect: { alpha: { type: "api_key", key: "a" } }, set: { alpha: { type: "api_key", key: "a2" } } }),
			backend,
		);
		const stored = JSON.parse(readFileSync(join(dir, "auth.json"), "utf-8"));
		expect(stored).toEqual({ alpha: { type: "api_key", key: "a2" } });
	});

	it("refuses an apply whose expect does not match and writes nothing", async () => {
		await runAuthStorageCommand(
			storageCommand(["auth", "set", "--provider", "alpha"]),
			JSON.stringify({ type: "api_key", key: "a" }),
			backend,
		);
		await expect(
			runAuthStorageCommand(
				storageCommand(["auth", "apply"]),
				JSON.stringify({ expect: {}, set: { beta: { type: "api_key", key: "b" } } }),
				backend,
			),
		).rejects.toThrow("changed");
		const stored = JSON.parse(readFileSync(join(dir, "auth.json"), "utf-8"));
		expect(stored).toEqual({ alpha: { type: "api_key", key: "a" } });
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
