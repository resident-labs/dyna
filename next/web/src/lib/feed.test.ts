import { test } from "node:test";
import { decodeEvent } from "./event.ts";
import { dmOf } from "./window.svelte.ts";
import assert from "node:assert/strict";
import { commandLine, dayLabel, describe, feedRows as dayRows, fileSize, formatTime, fromNow, isImage, quoted, requestLine, shortPaths, workSummary } from "./feed.ts";
import type { BoxState, Event, EventBody, FailureCause, Principal } from "./contract.gen.ts";

test("a forward quotes every line of the post under its label, then the owner's words", () => {
  const at = "2026-10-30T12:00:00Z";
  assert.equal(quoted({ author: "lead", at, text: "- one\n\n@root two\rthree", attachments: [] }, " see this \n"),
    `> **Forwarded from lead** · ${formatTime(at)}\n>\n> - one\n>\n> @root two\n> three\n\nsee this`);
});

test("a single-host grant for another session keeps who gets access and for how long", () => {
  const target = { kind: "host", name: "build-server" } as const;
  assert.deepEqual(requestLine({ body: "Let builder-feed run any command on build-server for 24 hours.\nReason: tests", target }),
    { text: "Let builder-feed run any command on ", named: "build-server", after: " for 24 hours." });
  assert.equal(requestLine({ body: "timeout: 30s", target }).named, "");
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
  assert.equal(work.kind === "work" && workSummary(work.events, session => names[session]), "lead-web worked · 2 turns");
});

test("names two sessions that ran turns and counts more", () => {
  const started = (seq: number, session: string) => event(seq, { type: "turn.started", body: { session, turn: `t${seq}`, inputs: [] } });
  const begun = (seq: number, session: string) => event(seq, { type: "session.started", body: { session, name: session, profile: "p", goal: "" } });
  const name = (session: string) => session;
  assert.equal(workSummary([started(1, "designer"), started(2, "lead-web")], name), "designer and lead-web worked · 2 turns");
  assert.equal(workSummary([begun(1, "a"), started(2, "a"), started(3, "b"), started(4, "c")], name), "a started · 3 sessions worked · 3 turns");
});

test("collapses repeated names in the work line", () => {
  const started = (seq: number, session: string) => event(seq, { type: "turn.started", body: { session, turn: `t${seq}`, inputs: [] } });
  const unknown = () => "A closed session";
  assert.equal(workSummary([started(1, "gone-1"), started(2, "gone-2")], unknown), "Closed sessions worked · 2 turns");
  assert.equal(workSummary([started(1, "gone"), started(2, "lead-web")], session => session === "gone" ? "A closed session" : session), "A closed session and lead-web worked · 2 turns");
});

test("a stopped turn and a room's pause show as their own lines", () => {
  const stopped = event(1, { type: "turn.ended", body: { session: "s", turn: "t", outcome: "stopped", usage } });
  const rows = feedRows([stopped, event(2, { type: "room.paused", body: {} }), event(3, { type: "room.resumed", body: {} })], { kind: "feed" });
  assert.deepEqual(rows.flatMap(row => row.kind === "notice" ? [describe(row.event, id => id)] : []), ["s's turn stopped", "Room paused", "Room resumed"]);
  assert.deepEqual(feedRows([stopped], { kind: "dm", session: "s" }).map(row => row.kind), ["work", "notice"]);
  assert.deepEqual(feedRows([event(2, { type: "room.paused", body: {} })], { kind: "dm", session: "s" }), []);
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
  const rows = feedRows([box(1, "ready"), box(2, "lost"), box(3, "failed", "no_hello"), box(4, "closing"), box(5, "closing", "archive_failed"), box(6, "stopped", "no_hello")], { kind: "feed" });
  assert.deepEqual(rows.map(row => row.kind === "failed" && [row.label, row.cause]), [["Stopped unexpectedly", ""],
    ["Failed", "Its workspace did not connect in time"], ["Could not close", "Dyna could not save the session's branch"], ["Could not start", "Its workspace did not connect in time"]]);
});

test("a failed turn with a final reply keeps its failed row", () => {
  const ended = event(1, { type: "turn.ended", body: { session: "l", turn: "t", outcome: "failed", final: "Partial", usage, error: "Broke",
    cause: "model_failed", replies: [{ reply: { kind: "final", target: { kind: "owner" } } }] } });
  const rows = feedRows([ended], { kind: "feed" });
  assert.deepEqual(rows.map(row => row.kind), ["post", "failed"]);
  assert.deepEqual(rows[1], { kind: "failed", key: "f1", seq: 1, session: "l", at: ended.at, label: "Turn failed", cause: "The model provider refused or failed the request" });
});

test("puts a turn's final reply in its home only, and a DM keeps a work row above it so the turn can open", () => {
  const ended = event(3, { type: "turn.ended", body: { session: "l", turn: "t", outcome: "completed", final: "Answer", usage,
    replies: [{ reply: { kind: "final", target: { kind: "owner" }, home: { kind: "dm", session: "l" } } }] } });
  const dm = feedRows([ended], { kind: "dm", session: "l" });
  assert.deepEqual(dm.map(row => row.kind === "post" ? row.text : row.kind), ["work", "Answer"]);
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

test("puts one New row before the first later post by someone else, and none without a read position", () => {
  const post = (seq: number, from: Principal) => event(seq, { type: "post.created", body: { text: String(seq), home: { kind: "feed" } } }, from);
  const events = [post(1, lead), post(2, { kind: "owner" }), post(3, { kind: "owner" }), post(4, lead), post(5, lead)];
  const kinds = (since: number) => feedRows(events, { kind: "feed" }, [], since).map(row => row.kind === "post" ? row.text : row.kind);
  assert.deepEqual(kinds(1), ["1", "2", "3", "new", "4", "5"]);
  assert.deepEqual(kinds(0), ["1", "2", "3", "4", "5"]);
});

test("a request is its own row where it happened, and its decision updates that row instead of folding into work", () => {
  const card = { title: "asks for host access", target: { kind: "room" as const, name: "r" } };
  const decision = { by: { kind: "owner" as const }, approved: true, at: "" };
  const rows = feedRows([
    event(1, { type: "turn.started", body: { session: "l", turn: "t1", inputs: [] } }),
    event(2, { type: "op.requested", body: { id: "op1", kind: "hostexec.grant", by: lead, class: "owner", card, expires: "" } }, lead),
    event(3, { type: "op.decided", body: { id: "op1", state: "approved", decision } }, { kind: "owner" }),
    event(4, { type: "turn.ended", body: { session: "l", turn: "t1", outcome: "completed", usage } }),
  ], { kind: "feed" });
  assert.deepEqual(rows.map(row => row.kind), ["work", "request", "work"]);
  assert.deepEqual(rows[1].kind === "request" && rows[1].request, { id: "op1", kind: "hostexec.grant", by: lead, class: "owner", card, expires: "", state: "approved", decision });
});

test("says how long the work in a row took, from the first turn's start to the last turn's end", () => {
  const at = (seq: number, minutes: number, body: EventBody) => ({ ...event(seq, body), at: new Date(minutes * 60_000).toISOString() });
  const ended = (seq: number, minutes: number, turn: string) => at(seq, minutes, { type: "turn.ended", body: { session: "l", turn, outcome: "completed", usage } });
  const events = [
    at(1, 0, { type: "turn.started", body: { session: "l", turn: "t1", inputs: [] } }), ended(2, 2, "t1"),
    at(3, 3, { type: "shell.job.ended", body: { session: "l", job: "j", exitCode: 0, tail: "" } }), ended(4, 6, "t2"),
  ];
  assert.equal(workSummary(events, session => names[session]), "lead-web worked for 6 min · 2 turns · 1 job");
  assert.equal(workSummary([events[0], ended(2, 80, "t1")], session => names[session]), "lead-web worked for 1 h 20 min · 1 turn");
  // A turn that answers here ends in the answer's row, which still gives the work row above it its end.
  const answered = at(2, 4, { type: "turn.ended", body: { session: "l", turn: "t1", outcome: "completed", usage, final: "Done", replies: [{ reply: { kind: "final" } }] } });
  const rows = dayRows([events[0], answered], { kind: "feed" });
  const work = rows.find(row => row.kind === "work");
  assert.equal(work?.kind === "work" && workSummary(work.events, session => names[session], work.until), "lead-web worked for 4 min · 1 turn");
});

test("a long path in an activity label keeps its first folder and its file, and short paths stay whole", () => {
  assert.equal(shortPaths("editing next/engine/approve/store/files.go"), "editing next/…/store/files.go");
  assert.equal(shortPaths("editing next/engine/files.go and web/src/a.ts"), "editing next/engine/files.go and web/src/a.ts");
});

test("a decoded restart appears as a quiet row in the session DM", () => {
  const notice = decodeEvent(event(2, { type: "session.restarted", body: { session: "l" } }));
  assert.ok(notice);
  assert.equal(dmOf(notice), "l");
  assert.deepEqual(feedRows([notice], { kind: "dm", session: "l" }, [], 1).map(row => row.kind), ["restarted"]);
});
