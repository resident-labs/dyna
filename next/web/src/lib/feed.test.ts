import { test } from "node:test";
import assert from "node:assert/strict";
import { commandLine, feedRows, workSummary } from "./feed.ts";
import type { Event, EventBody, Principal } from "./contract.gen.ts";

test("a command line shows each argument exactly: spaces, quotes and empty arguments are quoted", () => {
  assert.equal(commandLine(["tool", "a b"]), "tool 'a b'");
  assert.equal(commandLine(["tool", "a", "b"]), "tool a b");
  assert.equal(commandLine(["echo", "it's", "", "--x=1"]), `echo 'it'\\''s' '' --x=1`);
});

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
  assert.equal(work.kind === "work" && workSummary(work.events, session => names[session]), "lead-web ran 2 turns");
});

test("names two sessions that ran turns and counts more", () => {
  const started = (seq: number, session: string) => event(seq, { type: "turn.started", body: { session, turn: `t${seq}`, inputs: [] } });
  const begun = (seq: number, session: string) => event(seq, { type: "session.started", body: { session, name: session, profile: "p", goal: "" } });
  const name = (session: string) => session;
  assert.equal(workSummary([started(1, "designer"), started(2, "lead-web")], name), "designer and lead-web ran turns");
  assert.equal(workSummary([begun(1, "a"), started(2, "a"), started(3, "b"), started(4, "c")], name), "a started, 3 sessions ran turns");
});

test("names failed image builds and computers in the work line", () => {
  const image = { host: "h", repo: "r", name: "base", tag: "1" };
  const failed = event(1, { type: "image.failed", body: { ...image, reason: "no space", logTail: "" } });
  assert.equal(workSummary([failed], session => session), "1 image build failed");
  const computer = event(2, { type: "computer.changed", body: { session: "l", to: "running" } });
  assert.equal(workSummary([computer], session => names[session]), "lead-web opened a computer");
});

test("keeps box changes out of the feed except a failed or lost box", () => {
  const box = (seq: number, to: "ready" | "lost") => event(seq, { type: "box.changed", body: { box: "b", session: "l", to, reason: "podman: exit 125" } });
  const rows = feedRows([box(1, "ready"), box(2, "lost")], { kind: "feed" });
  assert.deepEqual(rows.map(row => row.kind === "failed" && [row.label, row.error]), [["Stopped unexpectedly", ""]]);
});

test("a failed turn with a final reply keeps its failed row", () => {
  const ended = event(1, { type: "turn.ended", body: { session: "l", turn: "t", outcome: "failed", final: "Partial", usage, error: "Broke",
    replies: [{ reply: { kind: "final", target: { kind: "owner" } } }] } });
  assert.deepEqual(feedRows([ended], { kind: "feed" }).map(row => row.kind), ["post", "failed"]);
});

test("puts a turn's final reply in its home only", () => {
  const ended = event(3, { type: "turn.ended", body: { session: "l", turn: "t", outcome: "completed", final: "Answer", usage,
    replies: [{ reply: { kind: "final", target: { kind: "owner" }, home: { kind: "dm", session: "l" } } }] } });
  const dm = feedRows([ended], { kind: "dm", session: "l" });
  assert.deepEqual(dm.map(row => row.kind === "post" && row.text), ["Answer"]);
  assert.deepEqual(feedRows([ended], { kind: "feed" }).map(row => row.kind), ["work"]);
});

test("names every mention and starts a new run when the mentions change", () => {
  const root: Principal = { kind: "session", session: "r" };
  const owner: Principal = { kind: "owner" };
  const to = (seq: number, mentions: Principal[]) => event(seq, { type: "post.created", body: { text: "x", home: { kind: "feed" }, mentions } }, owner);
  const rows = feedRows([to(1, [lead, root]), to(2, [lead, root]), to(3, [lead])], { kind: "feed" });
  assert.deepEqual(rows.map(row => row.kind === "post" && [row.first, row.to.length]), [[true, 2], [false, 2], [true, 1]]);
});
