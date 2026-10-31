import test from "node:test";
import assert from "node:assert/strict";
import { readWork } from "./session-work.ts";

test("work read skips invalid turns and preserves paging", async () => {
  const original = globalThis.fetch;
  globalThis.fetch = async () => new Response(JSON.stringify({
    turns: [
      { started: { seq: 1, id: "e1", at: "2026-01-01T00:00:00Z", room: "r", from: { kind: "engine" }, type: "turn.started", body: { session: "s", turn: "t1", inputs: [] } }, inputs: [], posts: [] },
      { started: { type: "unknown" }, inputs: [], posts: [] },
    ],
    more: true,
    total: 2,
  }), { status: 200 });
  try {
    const result = await readWork("s");
    assert.equal(result.turns.length, 1);
    assert.equal(result.turns[0].started.body.turn, "t1");
    assert.equal(result.more, true);
    assert.equal(result.total, 2);
  } finally {
    globalThis.fetch = original;
  }
});
