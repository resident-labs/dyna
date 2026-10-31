import { describe, expect, it } from "vitest";
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

	it("derives only the active branch and replaces it at compaction", () => {
		const session = SessionManager.inMemory();
		const head = session.appendMessage({ role: "system", content: "", sections: { goal: "first" }, timestamp: 0 });
		const update = createContextUpdate({ goal: "first" }, { goal: "second" });
		if (!update) throw new Error("expected update");
		session.appendMessage(update);
		expect(deliveredSections(session.getBranch())).toEqual({ goal: "second" });
		session.branch(head);
		expect(deliveredSections(session.getBranch())).toEqual({ goal: "first" });
		session.appendCompaction("summary", null, 0, undefined, false, undefined, { goal: "folded" });
		expect(deliveredSections(session.getBranch())).toEqual({ goal: "folded" });
	});
});
