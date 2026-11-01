import { test } from "node:test";
import assert from "node:assert/strict";
import { TurnTranscripts } from "./transcripts.svelte.ts";
import type { Event, TranscriptStep } from "./contract.gen.ts";

test("Work tab marks the 200-step limit before following another cursor", async () => {
  const events: Event[] = [{ seq: 1, id: "event", at: "2026-01-01T00:00:00Z", from: { kind: "owner" }, type: "turn.started", body: { session: "s", turn: "t", inputs: [] } }];
  const step: TranscriptStep = { turn: "t", tool: "read", label: "read", started: "2026-01-01T00:00:00Z", failed: false, preview: "" };
  const original = globalThis.fetch;
  let count = 0;
  globalThis.fetch = async (_input, init) => {
    if (count === 0) assert.doesNotMatch(String(init?.body), /"page":/);
    if (count === 6) assert.match(String(init?.body), /"page":"older_items","cursor":6/);
    const size = Math.min(6, 200 - count);
    count += size;
    return Response.json({ steps: Array.from({ length: size }, () => step), turns: [{ more: true, nextItem: count, ...(count === 200 ? { after: "input:next" } : {}) }] });
  };
  try {
    const reads = new TurnTranscripts("s");
    await reads.read(events);
    const result = reads.of(events);
    if (!result || typeof result === "string") assert.fail("Work transcript did not load");
    assert.equal(result.steps.length, 200);
    assert.equal(result.truncated, true);
  } finally { globalThis.fetch = original; }
});
