import { describe, expect, it } from "vitest";
import { ContextFileCache } from "../src/core/context-file-cache.ts";
import { createContextUpdate, deliveredSections } from "../src/core/context-updates.ts";
import { SessionManager } from "../src/core/session-manager.ts";

describe("context updates", () => {
	it("sends new, changed and removed sections together and stores the delivered state", () => {
		const current = { instructions: "first\nnew\n", goal: "ship" };
		const update = createContextUpdate({ instructions: "first\nold\n", obsolete: "x" }, current);
		expect(update?.content).toContain(" first\n-old\n+new");
		expect(update?.content).toContain("New section goal:\nship");
		expect(update?.content).toContain("Removed section obsolete.");
		expect(update?.details).toEqual({ sections: current });
		expect(createContextUpdate(current, current)).toBeUndefined();
	});

	it("limits unchanged context around section changes", () => {
		const old = ["distant", "two", "three", "four", "five", "old", "seven", "eight", "nine", "far away"].join("\n");
		const update = createContextUpdate({ rules: old }, { rules: old.replace("old", "new") });
		expect(update?.content).toContain("-old\n+new");
		expect(update?.content).not.toContain("distant");
		expect(update?.content).not.toContain("far away");
	});

	it("does no reads or diffs when file metadata is unchanged", () => {
		let reads = 0;
		const stamp = { ino: 1, size: 4, mtimeMs: 1, ctimeMs: 1, isFile: true };
		const cache = new ContextFileCache(
			() => stamp,
			() => {
				reads++;
				return "same";
			},
		);
		expect(cache.changed(["file"])).toBe(true);
		const first = cache.readFile("file");
		expect(cache.changed(["file"])).toBe(false);
		const second = cache.readFile("file");
		let diffs = 0;
		const update = createContextUpdate(
			{ file: first ?? "" },
			{ file: second ?? "" },
			{
				diff: () => {
					diffs++;
					return "";
				},
			},
		);
		expect(update).toBeUndefined();
		expect([reads, diffs]).toEqual([1, 0]);
	});

	it("removes retained pre-compaction updates already folded into the head", () => {
		const session = SessionManager.inMemory();
		session.appendMessage({ role: "system", content: "", sections: { goal: "old" }, timestamp: 0 });
		const kept = session.appendMessage({ role: "user", content: "question", timestamp: 1 });
		const update = createContextUpdate({ goal: "old" }, { goal: "middle" });
		if (!update) throw new Error("expected update");
		session.appendCustomMessageEntry(update.customType, update.content, update.display, update.details);
		session.appendCompaction("summary", kept, 0, undefined, false, undefined, { goal: "latest" });
		const messages = session.buildSessionContext().messages;
		expect(messages[0]).toMatchObject({ role: "system", sections: { goal: "latest" } });
		expect(messages.some((message) => message.role === "custom" && message.customType === "context_update")).toBe(
			false,
		);
	});

	it("derives only the active branch and replaces it at compaction", () => {
		const session = SessionManager.inMemory();
		const head = session.appendMessage({ role: "system", content: "", sections: { goal: "first" }, timestamp: 0 });
		const update = createContextUpdate({ goal: "first" }, { goal: "second" });
		if (!update) throw new Error("expected update");
		session.appendCustomMessageEntry(update.customType, update.content, update.display, update.details);
		expect(deliveredSections(session)).toEqual({ goal: "second" });
		let lookups = 0;
		expect(deliveredSections({
			getLeafEntry: () => session.getLeafEntry(),
			getEntry: (id) => { lookups++; return session.getEntry(id); },
		})).toEqual({ goal: "second" });
		expect(lookups).toBe(0);
		session.branch(head);
		expect(deliveredSections(session)).toEqual({ goal: "first" });
		session.appendCompaction("summary", null, 0, undefined, false, undefined, { goal: "folded" });
		expect(deliveredSections(session)).toEqual({ goal: "folded" });
	});
});
