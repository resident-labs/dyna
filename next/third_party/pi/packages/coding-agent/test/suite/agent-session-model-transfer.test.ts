import { mkdtempSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import {
	createAssistantMessageEventStream,
	fauxAssistantMessage,
	type Model,
	type SimpleStreamOptions,
	type TranscriptContext,
} from "@earendil-works/pi-ai";
import { afterEach, describe, expect, it, vi } from "vitest";
import { SessionManager } from "../../src/core/session-manager.ts";
import { createHarness, getMessageText, type Harness, type HarnessOptions } from "./harness.ts";

const small = { id: "small", contextWindow: 10_000, reasoning: true };
const large = { id: "large", contextWindow: 200_000, reasoning: true };
const huge = { id: "huge", contextWindow: 1_000_000, reasoning: true };
const compaction = { reserveTokens: 1_000, keepRecentTokens: 20_000 };

/** Whole turns of `size` tokens each, attributed to `model`. Returns the entry ID of each user message. */
function seedTurns(harness: Harness, model: Model<string>, turns: number, size: number): string[] {
	const ids: string[] = [];
	for (let i = 0; i < turns; i++) {
		ids.push(
			harness.sessionManager.appendMessage({
				role: "user",
				content: [{ type: "text", text: `question ${i} `.padEnd(size * 2, "x") }],
				timestamp: Date.now() - 2000,
			}),
		);
		const assistant = fauxAssistantMessage(`answer ${i} `.padEnd(size * 2, "y"), { timestamp: Date.now() - 1000 });
		harness.sessionManager.appendMessage({
			...assistant,
			api: model.api,
			provider: model.provider,
			model: model.id,
			usage: { ...assistant.usage, input: size * (i + 1), totalTokens: size * (i + 1) },
		});
	}
	harness.session.agent.state.messages = harness.sessionManager.buildSessionContext().messages;
	return ids;
}

interface SummaryRequest {
	model: string;
	context: TranscriptContext;
	options: SimpleStreamOptions | undefined;
}

/** Answers summary requests with `reply`, or fails them when `reply` is an Error. */
function stubSummaries(harness: Harness, reply: string | Error): SummaryRequest[] {
	const requests: SummaryRequest[] = [];
	harness.session.agent.streamFunction = (model, context, options) => {
		requests.push({ model: model.id, context, options });
		const stream = createAssistantMessageEventStream();
		queueMicrotask(() => {
			if (reply instanceof Error) {
				const message = fauxAssistantMessage("", { stopReason: "error", errorMessage: reply.message });
				stream.push({ type: "error", reason: "error", error: message });
			} else {
				const message = fauxAssistantMessage(reply, { stopReason: "stop" });
				stream.push({ type: "done", reason: "stop", message });
			}
		});
		return stream;
	};
	return requests;
}

/** A summary request that only ends when its signal aborts. Returns whether one has started. */
function hangSummaries(harness: Harness): () => boolean {
	let started = false;
	harness.session.agent.streamFunction = (_model, _context, options) => {
		started = true;
		const stream = createAssistantMessageEventStream();
		const end = () => {
			const message = fauxAssistantMessage("", { stopReason: "aborted", errorMessage: "aborted" });
			stream.push({ type: "error", reason: "aborted", error: message });
		};
		if (options?.signal?.aborted) end();
		else options?.signal?.addEventListener("abort", end);
		return stream;
	};
	return () => started;
}

/** Yields to real I/O until `done` holds, which also works under fake timers. */
async function until(done: () => boolean): Promise<void> {
	while (!done()) await new Promise((resolve) => setImmediate(resolve));
}

/** The first context message after compaction, as the model reads it. */
function boundaryText(harness: Harness): string {
	const first = harness.session.messages[0];
	return first?.role === "compactionSummary" ? first.summary : getMessageText(first);
}

describe("AgentSession.transferModel", () => {
	const harnesses: Harness[] = [];
	const directories: string[] = [];
	const persisted = (): SessionManager => {
		const directory = mkdtempSync(join(tmpdir(), "pi-model-transfer-"));
		directories.push(directory);
		return SessionManager.create(directory, directory);
	};
	const create = async (options: HarnessOptions = {}): Promise<Harness> => {
		const harness = await createHarness({
			models: [large, small],
			tools: [],
			settings: { compaction },
			...options,
		});
		harnesses.push(harness);
		return harness;
	};

	afterEach(() => {
		vi.restoreAllMocks();
		while (harnesses.length) harnesses.pop()?.cleanup();
		while (directories.length) rmSync(directories.pop()!, { recursive: true, force: true });
	});

	it("switches at once when the context fits", async () => {
		const harness = await create();
		seedTurns(harness, harness.getModel("large")!, 1, 100);
		const requests = stubSummaries(harness, "unused");

		const result = await harness.session.transferModel(harness.getModel("small")!, { thinkingLevel: "high" });

		expect(result.path).toBe("switched");
		expect(harness.session.model?.id).toBe("small");
		expect(harness.session.thinkingLevel).toBe("high");
		expect(requests).toHaveLength(0);
		expect(harness.eventsOfType("compaction_start")).toHaveLength(0);
		expect(
			harness.sessionManager
				.getBranch()
				.filter((entry) => entry.type === "model_change")
				.at(-1),
		).toMatchObject({
			modelId: "small",
		});
	});

	it("records nothing when the model stays and the context fits", async () => {
		const harness = await create();
		seedTurns(harness, harness.getModel("large")!, 1, 100);
		harness.session.setThinkingLevel("low");
		const before = harness.sessionManager.getEntries().length;

		const result = await harness.session.transferModel(harness.getModel("large")!, { thinkingLevel: "low" });

		expect(result.path).toBe("unchanged");
		expect(harness.sessionManager.getEntries()).toHaveLength(before);
	});

	it("compacts on the current model when the context does not fit the target", async () => {
		const harness = await create();
		const ids = seedTurns(harness, harness.getModel("large")!, 6, 3_000);
		const requests = stubSummaries(harness, "the real summary");

		const result = await harness.session.transferModel(harness.getModel("small")!, { thinkingLevel: "medium" });

		expect(result).toMatchObject({ path: "compacted", tokensBefore: 18_000 });
		expect(result.tokensAfter).toBeLessThan(9_000);
		expect(new Set(requests.map((request) => request.model))).toEqual(new Set(["large"]));
		expect(harness.session.model?.id).toBe("small");
		expect(boundaryText(harness)).toContain("the real summary");
		expect(harness.eventsOfType("compaction_start")).toEqual([{ type: "compaction_start", reason: "threshold" }]);
		expect(harness.eventsOfType("compaction_end")[0]).toMatchObject({ aborted: false, willRetry: false });
		// The model change follows the compaction, so a failed switch would leave the old model summarized.
		const types = harness.sessionManager.getBranch().map((entry) => entry.type);
		expect(types.lastIndexOf("model_change")).toBeGreaterThan(types.lastIndexOf("compaction"));
		expect(harness.sessionManager.getEntry(ids[0]!)).toBeDefined();
	});

	it.each([
		["a provider error", new Error("quota exhausted"), "quota exhausted"],
		["a refused model", new Error("403 The profile does not allow this model."), "does not allow this model"],
	])("leaves a boundary after %s", async (_name, reply, reason) => {
		const sessionManager = persisted();
		const harness = await create({ sessionManager });
		const model = harness.getModel("large")!;
		const ids = seedTurns(harness, model, 6, 3_000);
		const requests = stubSummaries(harness, reply);

		const result = await harness.session.transferModel(harness.getModel("small")!, { thinkingLevel: "medium" });

		expect(result.path).toBe("boundary");
		expect(result.reason).toContain(reason);
		expect(harness.session.model?.id).toBe("small");
		const file = sessionManager.getSessionFile()!;
		const boundary = boundaryText(harness);
		expect(boundary).toContain(file);
		expect(boundary).toContain("Read it");
		// Only the failed attempt reached a model: the boundary itself makes no request.
		expect(new Set(requests.map((request) => request.model))).toEqual(new Set(["large"]));
		// Pi's normal events: the failed summary, then the boundary.
		expect(harness.eventsOfType("compaction_end").map((event) => event.errorMessage !== undefined)).toEqual([
			true,
			false,
		]);
		// The tail the cut point kept, and the whole conversation on disk.
		expect(result.tokensAfter).toBeLessThan(9_000);
		const texts = harness.session.messages.map(getMessageText);
		expect(texts.some((text) => text.startsWith("answer 5"))).toBe(true);
		expect(texts.some((text) => text.startsWith("question 0"))).toBe(false);
		const onDisk = readFileSync(file, "utf8")
			.trim()
			.split("\n")
			.map((line) => JSON.parse(line));
		for (const id of ids) expect(onDisk.some((entry) => entry.id === id)).toBe(true);
		expect(onDisk.filter((entry) => entry.type === "compaction").at(-1).summary).toBe(
			harness.eventsOfType("compaction_end").at(-1)?.result?.summary,
		);
	});

	it("resumes the boundary from the session file on the new model", async () => {
		const sessionManager = persisted();
		const harness = await create({ sessionManager });
		seedTurns(harness, harness.getModel("large")!, 6, 3_000);
		stubSummaries(harness, new Error("quota exhausted"));
		await harness.session.transferModel(harness.getModel("small")!, { thinkingLevel: "medium" });
		const file = sessionManager.getSessionFile()!;

		const reopened = SessionManager.open(file);
		const resumed = await create({ sessionManager: reopened });

		expect(boundaryText(resumed)).toContain(file);
		expect(
			reopened
				.getBranch()
				.filter((entry) => entry.type === "model_change")
				.at(-1),
		).toMatchObject({
			modelId: "small",
		});
	});

	it("leaves a boundary when the summary is not enough", async () => {
		const harness = await create();
		seedTurns(harness, harness.getModel("large")!, 6, 3_000);
		stubSummaries(harness, "s".repeat(80_000));

		const result = await harness.session.transferModel(harness.getModel("small")!, { thinkingLevel: "medium" });

		expect(result).toMatchObject({ path: "boundary", reason: "the summary left too much context" });
		expect(result.tokensAfter).toBeLessThan(9_000);
		expect(boundaryText(harness)).not.toContain("sss");
	});

	it("keeps a summary that is over the trigger but under the hard limit", async () => {
		const harness = await create();
		seedTurns(harness, harness.getModel("large")!, 6, 3_000);
		stubSummaries(harness, "s".repeat(12_000));

		const result = await harness.session.transferModel(harness.getModel("small")!, {
			thinkingLevel: "medium",
			compactionTriggerTokens: 2_000,
		});

		expect(result.path).toBe("compacted");
	});

	it("compacts a conversation over 100k before a switch even when it fits, to keep the old cache", async () => {
		const harness = await create({ models: [large, huge] });
		seedTurns(harness, harness.getModel("large")!, 6, 20_000);
		const requests = stubSummaries(harness, "the real summary");

		const result = await harness.session.transferModel(harness.getModel("huge")!, { thinkingLevel: "medium" });

		expect(result).toMatchObject({ path: "compacted", tokensBefore: 120_000 });
		expect(new Set(requests.map((request) => request.model))).toEqual(new Set(["large"]));
		expect(harness.session.model?.id).toBe("huge");
	});

	it("leaves a conversation at 100k alone", async () => {
		const harness = await create({ models: [large, huge] });
		seedTurns(harness, harness.getModel("large")!, 5, 20_000);
		const requests = stubSummaries(harness, "unused");

		const result = await harness.session.transferModel(harness.getModel("huge")!, { thinkingLevel: "medium" });

		expect(result).toMatchObject({ path: "switched", tokensBefore: 100_000 });
		expect(requests).toHaveLength(0);
	});

	it("does not compact over 100k when the model stays, as on every box start", async () => {
		const harness = await create({ models: [large, huge] });
		seedTurns(harness, harness.getModel("large")!, 6, 20_000);
		const requests = stubSummaries(harness, "unused");

		const result = await harness.session.transferModel(harness.getModel("large")!, { thinkingLevel: "medium" });

		expect(result.path).toBe("unchanged");
		expect(requests).toHaveLength(0);
	});

	it("keeps a whole conversation that fits when its compaction fails", async () => {
		const harness = await create({ models: [large, huge] });
		seedTurns(harness, harness.getModel("large")!, 6, 20_000);
		stubSummaries(harness, new Error("quota exhausted"));

		const result = await harness.session.transferModel(harness.getModel("huge")!, { thinkingLevel: "medium" });

		expect(result).toMatchObject({ path: "switched", tokensAfter: 120_000 });
		expect(result.reason).toContain("quota exhausted");
		expect(harness.session.model?.id).toBe("huge");
		expect(harness.session.messages).toHaveLength(12);
		expect(harness.sessionManager.getBranch().some((entry) => entry.type === "compaction")).toBe(false);
	});

	it("keeps an earlier summary across a boundary", async () => {
		const harness = await create({ settings: { compaction: { reserveTokens: 1_000, keepRecentTokens: 3_000 } } });
		seedTurns(harness, harness.getModel("large")!, 6, 3_000);
		stubSummaries(harness, "the first summary");
		await harness.session.compact();
		seedTurns(harness, harness.getModel("large")!, 6, 3_000);
		stubSummaries(harness, new Error("quota exhausted"));

		const result = await harness.session.transferModel(harness.getModel("small")!, { thinkingLevel: "medium" });

		expect(result.path).toBe("boundary");
		expect(boundaryText(harness)).toContain("the first summary");
	});

	it("carries a good summary when the tail is what cannot fit", async () => {
		const harness = await create();
		seedTurns(harness, harness.getModel("large")!, 6, 3_000);
		harness.sessionManager.appendMessage({
			role: "user",
			content: [{ type: "text", text: "z".repeat(200_000) }],
			timestamp: Date.now(),
		});
		harness.session.agent.state.messages = harness.sessionManager.buildSessionContext().messages;
		stubSummaries(harness, "the real summary");

		const result = await harness.session.transferModel(harness.getModel("small")!, { thinkingLevel: "medium" });

		expect(result).toMatchObject({ path: "boundary", reason: "the summary left too much context" });
		expect(harness.session.messages).toHaveLength(1);
		expect(boundaryText(harness)).toContain("the real summary");
	});

	it("keeps no tail when one message cannot fit", async () => {
		const harness = await create();
		const model = harness.getModel("large")!;
		seedTurns(harness, model, 1, 100);
		harness.sessionManager.appendMessage({
			role: "user",
			content: [{ type: "text", text: "z".repeat(200_000) }],
			timestamp: Date.now(),
		});
		harness.session.agent.state.messages = harness.sessionManager.buildSessionContext().messages;
		stubSummaries(harness, new Error("quota exhausted"));

		const result = await harness.session.transferModel(harness.getModel("small")!, { thinkingLevel: "medium" });

		expect(result.path).toBe("boundary");
		expect(harness.session.messages).toHaveLength(1);
		expect(result.tokensAfter).toBeLessThan(1_000);
	});

	it("leaves a boundary when there is nothing to summarize", async () => {
		const harness = await create();
		harness.sessionManager.appendMessage({
			role: "user",
			content: [{ type: "text", text: "z".repeat(200_000) }],
			timestamp: Date.now(),
		});
		harness.session.agent.state.messages = harness.sessionManager.buildSessionContext().messages;
		const requests = stubSummaries(harness, "unused");

		const result = await harness.session.transferModel(harness.getModel("small")!, { thinkingLevel: "medium" });

		expect(result).toMatchObject({ path: "boundary", reason: "there was nothing to summarize" });
		expect(requests).toHaveLength(0);
		expect(harness.session.model?.id).toBe("small");
	});

	it("stays on the current model when aborted, and can run again", async () => {
		const harness = await create();
		seedTurns(harness, harness.getModel("large")!, 6, 3_000);
		const started = hangSummaries(harness);

		const transfer = harness.session.transferModel(harness.getModel("small")!, { thinkingLevel: "medium" });
		await until(started);
		await harness.session.abort();
		const result = await transfer;

		expect(result.path).toBe("aborted");
		expect(harness.session.model?.id).toBe("large");

		stubSummaries(harness, "the real summary");
		const retried = await harness.session.transferModel(harness.getModel("small")!, { thinkingLevel: "medium" });
		expect(retried.path).toBe("compacted");
	});

	it("gives up on a summary that hangs", async () => {
		const harness = await create();
		seedTurns(harness, harness.getModel("large")!, 6, 3_000);
		const started = hangSummaries(harness);
		vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
		try {
			const transfer = harness.session.transferModel(harness.getModel("small")!, { thinkingLevel: "medium" });
			await until(started);
			vi.advanceTimersByTime(10 * 60 * 1000 + 1);
			const result = await transfer;

			expect(result).toMatchObject({ path: "boundary", reason: "the summary timed out" });
			expect(harness.session.model?.id).toBe("small");
		} finally {
			vi.useRealTimers();
		}
	});

	it("compacts on the recorded model after a restart on the target", async () => {
		const harness = await create();
		const previous = harness.getModel("large")!;
		seedTurns(harness, previous, 6, 3_000);
		harness.sessionManager.appendModelChange(previous.provider, previous.id);
		// Box start: --model names the target, and the session file still records the old model.
		harness.session.agent.state.model = harness.getModel("small")!;
		const requests = stubSummaries(harness, "the real summary");

		const result = await harness.session.transferModel(harness.getModel("small")!, { thinkingLevel: "medium" });

		expect(result.path).toBe("compacted");
		expect(new Set(requests.map((request) => request.model))).toEqual(new Set(["large"]));
		expect(
			harness.sessionManager
				.getBranch()
				.filter((entry) => entry.type === "model_change")
				.at(-1),
		).toMatchObject({
			modelId: "small",
		});
	});

	it("records the target after a restart when the context fits", async () => {
		const harness = await create();
		const previous = harness.getModel("large")!;
		seedTurns(harness, previous, 1, 100);
		harness.sessionManager.appendModelChange(previous.provider, previous.id);
		harness.session.agent.state.model = harness.getModel("small")!;

		const result = await harness.session.transferModel(harness.getModel("small")!, { thinkingLevel: "medium" });

		expect(result.path).toBe("switched");
		expect(
			harness.sessionManager
				.getBranch()
				.filter((entry) => entry.type === "model_change")
				.at(-1),
		).toMatchObject({
			modelId: "small",
		});
	});

	it("applies the target's compaction trigger to the check and to later turns", async () => {
		const harness = await create();
		seedTurns(harness, harness.getModel("large")!, 4, 3_000);
		const requests = stubSummaries(harness, "the real summary");

		const first = await harness.session.transferModel(harness.getModel("large")!, {
			thinkingLevel: "medium",
			compactionTriggerTokens: 5_000,
		});
		expect(first.path).toBe("compacted");
		expect(requests.length).toBeGreaterThan(0);
		expect(first.tokensAfter).toBeLessThan(5_000);

		const internals = harness.session as unknown as {
			_compactionSettings: (model: Model<string>) => { triggerTokens?: number };
		};
		expect(internals._compactionSettings(harness.getModel("large")!).triggerTokens).toBe(5_000);

		await harness.session.transferModel(harness.getModel("large")!, { thinkingLevel: "medium" });
		expect(internals._compactionSettings(harness.getModel("large")!).triggerTokens).toBeUndefined();
	});

	it("does not touch the session when the target has no auth", async () => {
		const harness = await create();
		seedTurns(harness, harness.getModel("large")!, 6, 3_000);
		const requests = stubSummaries(harness, "unused");
		const target = { ...harness.getModel("small")!, provider: "nobody" };

		await expect(harness.session.transferModel(target, { thinkingLevel: "medium" })).rejects.toThrow("No API key");

		expect(requests).toHaveLength(0);
		expect(harness.session.model?.id).toBe("large");
	});
});
