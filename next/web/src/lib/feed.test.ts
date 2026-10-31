import { test } from "node:test";
import assert from "node:assert/strict";
import { feedRows, workSummary } from "./feed.ts";
import type { Event, EventBody, Principal } from "./contract.gen.ts";

const lead: Principal = { kind: "session", session: "l" };
function event(seq: number, body: EventBody, from: Principal = { kind: "engine" }): Event {
  return { seq, id: String(seq), at: "", from, ...body };
}
const usage = { model: "m", inputTokens: 0, outputTokens: 0, cacheReadTokens: 0, cacheWriteTokens: 0 };
const names: Record<string, string> = { l: "lead-web" };

test("folds the work between two posts into one row", () => {
  const rows = feedRows([
    event(1, { type: "post.created", body: { text: "go", home: { kind: "feed" } } }, { kind: "owner" }),
    event(2, { type: "turn.started", body: { session: "l", turn: "t1", inputs: [] } }),
    event(3, { type: "turn.ended", body: { session: "l", turn: "t1", outcome: "completed", usage } }),
    event(4, { type: "box.changed", body: { box: "b", session: "l", to: "ready" } }),
    event(5, { type: "turn.ended", body: { session: "l", turn: "t2", outcome: "completed", usage } }),
    event(6, { type: "turn.ended", body: { session: "l", turn: "t3", outcome: "failed", usage, error: "Broke" } }),
    event(7, { type: "post.created", body: { text: "done", home: { kind: "feed" } } }, lead),
  ], { kind: "feed" });
  assert.deepEqual(rows.map(row => row.kind), ["post", "work", "failed", "post"]);
  const work = rows[1];
  assert.equal(work.kind === "work" && workSummary(work.events, session => names[session]), "lead-web: 2 turns, box ready");
});

test("puts a turn's final reply in its home only", () => {
  const ended = event(3, { type: "turn.ended", body: { session: "l", turn: "t", outcome: "completed", final: "Answer", usage,
    replies: [{ reply: { kind: "final", target: { kind: "owner" }, home: { kind: "dm", session: "l" } } }] } });
  const dm = feedRows([ended], { kind: "dm", session: "l" });
  assert.deepEqual(dm.map(row => row.kind === "post" && row.text), ["Answer"]);
  assert.deepEqual(feedRows([ended], { kind: "feed" }).map(row => row.kind), ["work"]);
});
