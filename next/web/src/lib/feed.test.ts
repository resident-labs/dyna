import { test } from "node:test";
import assert from "node:assert/strict";
import { commandLine, dayLabel, feedRows as dayRows, fileSize, formatTime, fromNow, isImage, quoted, workSummary } from "./feed.ts";
import type { BoxState, Event, EventBody, FailureCause, Principal } from "./contract.gen.ts";

test("a forward quotes every line of the post under its label, then the owner's words", () => {
  const at = "2026-10-30T12:00:00Z";
  assert.equal(quoted({ author: "lead", at, text: "- one\n\n@root two\rthree", attachments: [] }, " see this \n"),
    `> **Forwarded from lead** · ${formatTime(at)}\n>\n> - one\n>\n> @root two\n> three\n\nsee this`);
});

test("a command line shows each argument exactly: spaces, quotes and empty arguments are quoted", () => {
  assert.equal(commandLine(["tool", "a b"]), "tool 'a b'");
  assert.equal(commandLine(["tool", "a", "b"]), "tool a b");
  assert.equal(commandLine(["echo", "it's", "", "--x=1"]), `echo 'it'\\''s' '' --x=1`);
});

test("a time less than a minute ahead is 'in under a minute', not 'just now'", () => {
  assert.equal(fromNow(new Date(20_000).toISOString(), 0), "in under a minute");
  assert.equal(fromNow(new Date(0).toISOString(), 20_000), "just now");
});

test("shows only browser image types as thumbnails, and sizes in B, KB and MB", () => {
  const file = (media: string) => ({ name: "f", media, bytes: 0, sha256: "" });
  assert.deepEqual(["image/png", "image/webp", "image/svg+xml", "text/plain"].map(media => isImage(file(media))), [true, true, false, false]);
  assert.deepEqual([512, 12_700, 3_250_000].map(fileSize), ["512 B", "12.4 KB", "3.1 MB"]);
});

const lead: Principal = { kind: "session", session: "l" };
function event(seq: number, body: EventBody, from: Principal = { kind: "engine" }): Event {
  return { seq, id: String(seq), at: "", from, ...body };
}
// These cases leave out the day rows; one case below covers them.
const feedRows = (...args: Parameters<typeof dayRows>) => dayRows(...args).filter(row => row.kind !== "day");
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

test("keeps box changes out of the feed except a failed or lost box or a failed close, in the owner's words", () => {
  const box = (seq: number, to: BoxState, cause?: FailureCause) => event(seq, { type: "box.changed", body: { box: "b", session: "l", to, cause, reason: "podman: exit 125" } });
  const rows = feedRows([box(1, "ready"), box(2, "lost"), box(3, "failed", "no_hello"), box(4, "closing"), box(5, "closing", "archive_failed")], { kind: "feed" });
  assert.deepEqual(rows.map(row => row.kind === "failed" && [row.label, row.cause]), [["Stopped unexpectedly", ""],
    ["Could not start", "Its workspace did not connect in time"], ["Could not close", "Dyna could not save the session's branch"]]);
});

test("a failed turn with a final reply keeps its failed row", () => {
  const ended = event(1, { type: "turn.ended", body: { session: "l", turn: "t", outcome: "failed", final: "Partial", usage, error: "Broke",
    cause: "model_failed", replies: [{ reply: { kind: "final", target: { kind: "owner" } } }] } });
  const rows = feedRows([ended], { kind: "feed" });
  assert.deepEqual(rows.map(row => row.kind), ["post", "failed"]);
  assert.deepEqual(rows[1], { kind: "failed", key: "f1", session: "l", at: ended.at, label: "Turn failed", cause: "The model provider refused or failed the request" });
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

test("starts each local day with a day row, which also starts a new name line", () => {
  const owner: Principal = { kind: "owner" };
  const at = (seq: number, day: number) => ({ ...event(seq, { type: "post.created", body: { text: "x", home: { kind: "feed" } } }, owner), at: new Date(2026, 9, day, 12).toISOString() });
  const rows = dayRows([at(1, 30), at(2, 30), at(3, 31)], { kind: "feed" });
  assert.deepEqual(rows.map(row => row.kind === "post" ? row.first : row.kind), ["day", true, false, "day", true]);
  const now = new Date(2026, 9, 31, 18).getTime();
  assert.deepEqual(rows.flatMap(row => row.kind === "day" ? [dayLabel(row.at, now)] : []), ["Yesterday", "Today"]);
  assert.equal(dayLabel(new Date(2026, 9, 24, 12).toISOString(), now), "Saturday, October 24");
});
