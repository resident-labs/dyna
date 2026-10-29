import { createInterface } from "node:readline";
import type { ModelRuntime } from "../core/model-runtime.ts";

export type AuthLoginEvent =
	| { type: "auth_url"; url: string; instructions?: string }
	| { type: "device_code"; verificationUri: string; userCode: string }
	| { type: "select"; message: string; options: readonly { id: string; label: string }[] }
	| { type: "prompt"; message: string }
	| { type: "progress"; message: string }
	| { type: "done" }
	| { type: "error"; message: string };

export type AuthRefreshResult =
	| { status: "renewed" }
	| { status: "no_credential" }
	| { status: "failed"; kind: "invalid_grant" | "network" | "timeout" | "unknown"; httpStatus: number };

export type AuthLineReader = (signal?: AbortSignal) => Promise<string>;

/** Runs Pi's OAuth flow as JSONL events, with answers read one line at a time. */
export async function runAuthLogin(
	runtime: ModelRuntime,
	providerId: string,
	readLine: AuthLineReader,
	emit: (event: AuthLoginEvent) => void,
): Promise<boolean> {
	try {
		const providers = runtime
			.getProviders()
			.flatMap((provider) =>
				provider.auth.oauth ? [{ id: provider.id, label: provider.name || provider.id }] : [],
			);
		let selected = providerId;
		if (!selected) {
			emit({ type: "select", message: "Choose an OAuth provider.", options: providers });
			selected = await readLine();
			if (!providers.some((provider) => provider.id === selected)) throw new Error("Invalid choice");
		}
		if (!providers.some((provider) => provider.id === selected)) throw new Error("Unknown OAuth provider");
		await runtime.login(selected, "oauth", {
			prompt: async (prompt) => {
				if (prompt.type === "select") {
					// Prefer Pi's native headless method when the provider offers it.
					if (prompt.options.some((option) => option.id === "copy_code")) return "copy_code";
					emit({ type: "select", message: prompt.message, options: prompt.options });
					const answer = await readLine(prompt.signal);
					if (!prompt.options.some((option) => option.id === answer)) throw new Error("Invalid choice");
					return answer;
				}
				emit({ type: "prompt", message: prompt.message });
				return readLine(prompt.signal);
			},
			notify: (event) => {
				switch (event.type) {
					case "auth_url":
						emit({ type: "auth_url", url: event.url, instructions: event.instructions });
						break;
					case "device_code":
						emit({ type: "device_code", verificationUri: event.verificationUri, userCode: event.userCode });
						break;
					case "info":
					case "progress":
						emit({ type: "progress", message: event.message });
						break;
				}
			},
		});
		emit({ type: "done" });
		return true;
	} catch {
		emit({ type: "error", message: "Pi OAuth sign-in did not finish. Try again." });
		return false;
	}
}

/** Renews a saved grant through Pi's provider runtime and its credential lock. */
export async function runAuthRefresh(runtime: ModelRuntime, providerId: string): Promise<AuthRefreshResult> {
	try {
		const resolution = await runtime.getAuth(providerId, { forceOAuthRefresh: true });
		if (!resolution) return { status: "no_credential" };
		return { status: "renewed" };
	} catch (error) {
		return classifyAuthRefreshFailure(error);
	}
}

function errorRecord(error: unknown): Record<string, unknown> | undefined {
	return typeof error === "object" && error !== null ? (error as Record<string, unknown>) : undefined;
}

function errorChain(error: unknown): Record<string, unknown>[] {
	const chain: Record<string, unknown>[] = [];
	let current: unknown = error;
	for (let depth = 0; depth < 8; depth++) {
		const record = errorRecord(current);
		if (!record) break;
		chain.push(record);
		current = record.cause;
	}
	return chain;
}

function errorMessage(record: Record<string, unknown>): string {
	return typeof record.message === "string" ? record.message : "";
}

function refreshHTTPStatus(chain: Record<string, unknown>[]): number {
	for (const record of chain) {
		const response = errorRecord(record.response);
		for (const value of [record.status, record.statusCode, response?.status]) {
			if (typeof value === "number" && value >= 100 && value <= 599) return value;
		}
		const match = /\((\d{3})\)/u.exec(errorMessage(record));
		if (match) return Number(match[1]);
	}
	return 0;
}

function refreshFailureKind(chain: Record<string, unknown>[]): "invalid_grant" | "network" | "timeout" | "unknown" {
	for (const record of chain) {
		const code = typeof record.code === "string" ? record.code : "";
		const text = `${errorMessage(record)} ${code}`;
		if (/invalid_grant|invalid_token|unauthorized/iu.test(text)) return "invalid_grant";
		if (/timeout|timed out|ETIMEDOUT|aborted/iu.test(text)) return "timeout";
		if (/fetch failed|ENOTFOUND|ECONNREFUSED|ECONNRESET|EAI_AGAIN|network/iu.test(text)) return "network";
	}
	return "unknown";
}

function classifyAuthRefreshFailure(error: unknown): AuthRefreshResult {
	const chain = errorChain(error);
	const httpStatus = refreshHTTPStatus(chain);
	let kind = refreshFailureKind(chain);
	if (httpStatus === 400 || httpStatus === 401 || httpStatus === 403) kind = "invalid_grant";
	return { status: "failed", kind, httpStatus };
}

/** Creates a queued line reader for headless login, including prompt cancellation. */
export function createAuthLineReader(input: NodeJS.ReadableStream): { readLine: AuthLineReader; close: () => void } {
	const rl = createInterface({ input, terminal: false });
	const queued: string[] = [];
	const pending: { resolve: (value: string) => void; reject: (error: Error) => void }[] = [];
	let ended = false;
	rl.on("line", (line) => {
		if (pending.length) pending.shift()?.resolve(line.trim());
		else queued.push(line.trim());
	});
	rl.on("close", () => {
		ended = true;
		for (const entry of pending.splice(0)) entry.reject(new Error("Input closed"));
	});
	function readLine(signal?: AbortSignal): Promise<string> {
		if (signal?.aborted) return Promise.reject(new Error("Login cancelled"));
		if (queued.length) return Promise.resolve(queued.shift()!);
		if (ended) return Promise.reject(new Error("Input closed"));
		return new Promise((resolve, reject) => {
			const entry = {
				resolve(value: string) {
					cleanup();
					resolve(value);
				},
				reject(error: Error) {
					cleanup();
					reject(error);
				},
			};
			const cleanup = () => signal?.removeEventListener("abort", abort);
			const abort = () => {
				const index = pending.indexOf(entry);
				if (index >= 0) pending.splice(index, 1);
				entry.reject(new Error("Login cancelled"));
			};
			pending.push(entry);
			signal?.addEventListener("abort", abort, { once: true });
		});
	}
	return { readLine, close: () => rl.close() };
}
