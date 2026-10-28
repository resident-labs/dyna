import type { AuthResult } from "@earendil-works/pi-ai";
import { APP_NAME } from "../config.ts";
import { FileAuthStorageBackend } from "../core/auth-storage.ts";
import { stripBom } from "../utils/text.ts";
import type { Args } from "./args.ts";

export type AuthCommandKind = "check" | "api_key" | "bearer_token" | "list" | "set" | "remove" | "apply";

export interface AuthCommand {
	kind: AuthCommandKind;
	args: string[];
	json: boolean;
	credentials: boolean;
	noRefresh: boolean;
	minExpiryMs?: number;
	provider?: string;
}

export class AuthCommandError extends Error {}

const AUTH_COMMAND_USAGE: Record<AuthCommandKind, string> = {
	check: `${APP_NAME} auth check --provider <provider> [--json] [--credentials] [--no-refresh]`,
	api_key: `${APP_NAME} auth print-api-key --provider <provider> [--model <model>]`,
	bearer_token: `${APP_NAME} auth print-bearer-token --provider <provider> [--model <model>] [--min-expiry <duration>]`,
	list: `${APP_NAME} auth list [--json]`,
	set: `${APP_NAME} auth set --provider <provider>`,
	remove: `${APP_NAME} auth remove --provider <provider>`,
	apply: `${APP_NAME} auth apply`,
};

export function getAuthCommandName(kind: AuthCommandKind): string {
	if (kind === "check") return "auth check";
	if (kind === "api_key") return "auth print-api-key";
	if (kind === "bearer_token") return "auth print-bearer-token";
	if (kind === "list") return "auth list";
	if (kind === "set") return "auth set";
	if (kind === "remove") return "auth remove";
	return "auth apply";
}

export function getAuthCommandUsage(kind: AuthCommandKind): string {
	return AUTH_COMMAND_USAGE[kind];
}

export function isAuthCommandHelp(args: string[]): boolean {
	return (
		args[0] === "auth" &&
		(args[1] === undefined || args[1] === "help" || args.includes("--help") || args.includes("-h"))
	);
}

export function printAuthCommandHelp(): void {
	console.log(`Usage:
  pi auth print-api-key [--provider <provider>] [--model <model>]
  pi auth print-bearer-token [--provider <provider>] [--model <model>] [--min-expiry <duration>]
  pi auth check [--provider <provider>] [--model <model>] [--json] [--credentials] [--no-refresh]
  pi auth list [--json]
  pi auth set --provider <provider>
  pi auth remove --provider <provider>
  pi auth apply

Auth commands require at least one of --provider or --model. Checks refresh expired OAuth credentials by default; --no-refresh prevents this. --credentials emits the credential, or includes it in JSON output.

Auth storage commands read and write the login file under its own lock: list prints the stored providers (--json prints the entries), set stores one entry read as JSON from stdin, remove drops one provider (a missing entry is not an error), apply performs a whole edit of one JSON object on stdin under one lock: an optional "set" object of entries, an optional "remove" array of provider ids, and an optional "expect" object that makes the command refuse unless the file still holds exactly it; apply prints the resulting entries as one JSON object.`);
}

export function parseAuthCommand(args: string[]): AuthCommand | undefined {
	if (args[0] !== "auth") return undefined;

	const kind =
		args[1] === "check"
			? "check"
			: args[1] === "print-api-key"
				? "api_key"
				: args[1] === "print-bearer-token"
					? "bearer_token"
					: args[1] === "list"
						? "list"
						: args[1] === "set"
							? "set"
							: args[1] === "remove"
								? "remove"
								: args[1] === "apply"
									? "apply"
									: undefined;
	if (!kind) {
		throw new AuthCommandError(
			`Unknown auth command "${args[1] ?? ""}". Use "${APP_NAME} auth help" for the supported commands.`,
		);
	}

	const commandArgs: string[] = [];
	let json = false;
	let credentials = false;
	let noRefresh = false;
	let minExpiryMs: number | undefined;
	let provider: string | undefined;
	for (let index = 2; index < args.length; index++) {
		const arg = args[index];
		if (arg === "--min-expiry") {
			if (kind !== "bearer_token")
				throw new AuthCommandError("--min-expiry is only supported by print-bearer-token");
			const value = args[++index];
			const match = value ? /^(\d+)(ms|s|m|h)$/iu.exec(value) : undefined;
			if (!match) throw new AuthCommandError("--min-expiry must use a duration such as 30m or 1h");
			const amount = Number(match[1]);
			const unit = match[2];
			minExpiryMs = amount * (unit === "ms" ? 1 : unit === "s" ? 1_000 : unit === "m" ? 60_000 : 3_600_000);
			continue;
		}
		if (arg === "--provider" && (kind === "set" || kind === "remove")) {
			provider = args[++index];
			if (!provider) throw new AuthCommandError(`--provider requires a value for "${getAuthCommandName(kind)}"`);
			continue;
		}
		if (arg === "--json") {
			if (kind !== "check" && kind !== "list")
				throw new AuthCommandError("--json is only supported by auth check and auth list");
			json = true;
			continue;
		}
		if (arg === "--credentials" || arg === "--no-refresh") {
			if (kind !== "check") throw new AuthCommandError(`${arg} is only supported by auth check`);
			if (arg === "--credentials") credentials = true;
			else noRefresh = true;
			continue;
		}
		commandArgs.push(arg);
	}

	if ((kind === "set" || kind === "remove") && !provider) {
		throw new AuthCommandError(`"${getAuthCommandName(kind)}" requires --provider <provider>`);
	}

	const command: AuthCommand =
		minExpiryMs === undefined
			? { kind, args: commandArgs, json, credentials, noRefresh }
			: { kind, args: commandArgs, json, credentials, noRefresh, minExpiryMs };
	if (provider !== undefined) command.provider = provider;
	return command;
}

/** The lock-aware backend surface the storage commands need. */
export type AuthStorageCommandBackend = Pick<FileAuthStorageBackend, "withLockAsync">;

function parseAuthEntries(current: string | undefined): Record<string, unknown> {
	let parsed: unknown;
	try {
		parsed = JSON.parse(stripBom(current ?? "{}"));
	} catch {
		throw new AuthCommandError("The login file is not valid JSON.");
	}
	if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed)) {
		throw new AuthCommandError("The login file is not a JSON object.");
	}
	return parsed as Record<string, unknown>;
}

/** One whole-file edit of the login file, read as JSON from apply's stdin. */
type AuthApplyEdit = {
	set?: Record<string, unknown>;
	remove?: string[];
	expect?: Record<string, unknown>;
};

function parseAuthApplyEdit(stdin: string | undefined): AuthApplyEdit {
	let parsed: unknown;
	try {
		if (stdin === undefined) throw new Error();
		parsed = JSON.parse(stripBom(stdin));
	} catch {
		throw new AuthCommandError("auth apply requires one JSON edit object on stdin.");
	}
	if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed)) {
		throw new AuthCommandError("auth apply requires one JSON object on stdin.");
	}
	const edit = parsed as AuthApplyEdit;
	if (edit.set !== undefined && (typeof edit.set !== "object" || edit.set === null || Array.isArray(edit.set))) {
		throw new AuthCommandError("auth apply \"set\" must be one object of entries.");
	}
	if (edit.remove !== undefined) {
		if (!Array.isArray(edit.remove) || edit.remove.some((id) => typeof id !== "string")) {
			throw new AuthCommandError("auth apply \"remove\" must be one array of provider ids.");
		}
	}
	if (edit.expect !== undefined && (typeof edit.expect !== "object" || edit.expect === null || Array.isArray(edit.expect))) {
		throw new AuthCommandError("auth apply \"expect\" must be one object of entries.");
	}
	return edit;
}

function applyAuthEdit(entries: Record<string, unknown>, edit: AuthApplyEdit): Record<string, unknown> {
	for (const id of edit.remove ?? []) delete entries[id];
	for (const [id, entry] of Object.entries(edit.set ?? {})) entries[id] = entry;
	return entries;
}

/** Reports whether two JSON values hold the same content: apply's expect check. */
function sameJsonValues(a: unknown, b: unknown): boolean {
	if (a === b) return true;
	if (typeof a !== "object" || typeof b !== "object" || a === null || b === null) return false;
	if (Array.isArray(a) !== Array.isArray(b)) return false;
	const keys = Object.keys(a as object);
	if (keys.length !== Object.keys(b as object).length) return false;
	return keys.every((key) => key in (b as object) && sameJsonValues((a as Record<string, unknown>)[key], (b as Record<string, unknown>)[key]));
}

/**
 * Runs the auth storage commands: list prints the stored providers, set stores
 * one entry read as JSON from stdin, remove drops one provider. Every command
 * reads and writes under the login file's own lock.
 */
export async function runAuthStorageCommand(
	command: AuthCommand,
	stdin: string | undefined,
	backend: AuthStorageCommandBackend = new FileAuthStorageBackend(),
): Promise<void> {
	if (command.kind === "list") {
		const entries = await backend.withLockAsync(async (current) => ({ result: parseAuthEntries(current) }));
		if (command.json) {
			console.log(JSON.stringify(entries));
		} else {
			for (const entry of Object.keys(entries).sort()) console.log(entry);
		}
		return;
	}
	if (command.kind === "apply") {
		const edit = parseAuthApplyEdit(stdin);
		const result = await backend.withLockAsync(async (current) => {
			const entries = parseAuthEntries(current);
			if (edit.expect !== undefined && !sameJsonValues(entries, edit.expect)) {
				throw new AuthCommandError("The login file changed since it was read.");
			}
			const next = applyAuthEdit(entries, edit);
			return { result: next, next: JSON.stringify(next, null, 2) };
		});
		console.log(JSON.stringify(result));
		return;
	}
	const provider = command.provider;
	if (provider === undefined) {
		throw new AuthCommandError(`"${getAuthCommandName(command.kind)}" requires --provider <provider>`);
	}
	if (command.kind === "remove") {
		await backend.withLockAsync(async (current) => {
			const entries = parseAuthEntries(current);
			delete entries[provider];
			return { result: undefined, next: JSON.stringify(entries, null, 2) };
		});
		return;
	}
	let entry: unknown;
	try {
		if (stdin === undefined) throw new Error();
		entry = JSON.parse(stdin);
	} catch {
		throw new AuthCommandError("auth set requires one entry as valid JSON on stdin.");
	}
	await backend.withLockAsync(async (current) => {
		const entries = parseAuthEntries(current);
		entries[provider] = entry;
		return { result: undefined, next: JSON.stringify(entries, null, 2) };
	});
}

export function validateAuthCommandArgs(args: Args, kind: AuthCommandKind): { provider?: string; model?: string } {
	const provider = args.provider?.trim() || undefined;
	const model = args.model?.trim() || undefined;
	if (args.unknownFlags.size > 0) {
		const option = args.unknownFlags.keys().next().value;
		throw new AuthCommandError(`Unknown option --${option} for "${getAuthCommandName(kind)}".`);
	}
	if (args.apiKey !== undefined || args.messages.length > 0 || args.fileArgs.length > 0) {
		throw new AuthCommandError("Auth commands only accept --provider and --model");
	}
	if (kind === "check") {
		if (!provider && !model) {
			throw new AuthCommandError("Auth checks require --provider <provider> or --model <model>");
		}
		return { provider, model };
	}
	if (!provider && !model) {
		throw new AuthCommandError("Credential printing requires --provider <provider> or --model <model>");
	}
	return { provider, model };
}

export function getAuthCredential(auth: AuthResult | undefined): string | undefined {
	if (auth?.auth.apiKey) return auth.auth.apiKey;
	const authorization = Object.entries(auth?.auth.headers ?? {}).find(
		([name]) => name.toLowerCase() === "authorization",
	)?.[1];
	return typeof authorization === "string" ? /^Bearer\s+(.+)$/iu.exec(authorization)?.[1] : undefined;
}
