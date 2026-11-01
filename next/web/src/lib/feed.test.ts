import { test } from "node:test";
import { decodeEvent } from "./event.ts";
import { dmOf } from "./window.svelte.ts";
import assert from "node:assert/strict";
import { clause, commandLine, dayLabel, describe, feedRows as dayRows, fileSize, formatTime, fromNow, isImage, isThumb, prWords, quoted, requestLine, shortPaths, subject, workIsBare, workSummary } from "./feed.ts";
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
  assert.equal(clause(" for 24 hours."), " for 24 hours");
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
test("a subscription shows in the feed only when it fails", () => {
  const passed = decodeEvent({ seq: 1, id: "a", at: "2026-10-31T12:00:00Z", from: { kind: "engine" }, type: "hook.finished", body: { subscription: "sub_a", result: "passed", exitCode: 0 } })!;
  const due = decodeEvent({ seq: 2, id: "b", at: "2026-10-31T12:00:01Z", from: { kind: "engine" }, type: "hook.due", body: { subscription: "sub_a" } })!;
  const failing = decodeEvent({ seq: 3, id: "c", at: "2026-10-31T12:00:02Z", from: { kind: "engine" }, type: "hook.finished", body: { subscription: "sub_a", result: "failed", exitCode: 3, failures: 1 } })!;
  assert.equal(subject(passed), undefined);
  assert.equal(describe(failing, id => id), "A subscription's run failed: exit code 3");
  assert.equal(dayRows([passed, due], { kind: "feed" }).length, 0);
  assert.ok(dayRows([failing], { kind: "feed" }).length > 0);
});

function event(seq: number, body: EventBody, from: Principal = { kind: "engine" }): Event {
  return { seq, id: String(seq), at: "", from, ...body };
}
// These cases leave out the day rows; one case below covers them.
const feedRows = (...args: Parameters<typeof dayRows>) => dayRows(...args).filter(row => row.kind !== "day");
const usage = { model: "m", inputTokens: 0, outputTokens: 0, cacheReadTokens: 0, cacheWriteTokens: 0 };
const names: Record<string, string> = { l: "lead-web" };

test("groups work, stopped turns and failures without letting dividers split the block", () => {
  const rows = feedRows([
    event(1, { type: "turn.started", body: { session: "l", turn: "t", inputs: [] } }),
    event(2, { type: "room.paused", body: {} }, { kind: "owner" }),
    event(3, { type: "turn.ended", body: { session: "l", turn: "t", outcome: "stopped", usage } }),
    event(4, { type: "turn.ended", body: { session: "l", turn: "f", outcome: "failed", usage, cause: "model_failed" } }),
    event(5, { type: "post.created", body: { text: "done", home: { kind: "feed" } } }, lead),
  ], { kind: "feed" });
  assert.deepEqual(rows.map(row => row.kind), ["work", "notice", "failed", "post"]);
  const work = rows[0];
  assert.equal(work.kind === "work" && workSummary(work.events, id => id), "l worked · 1 turn stopped");
});

test("places completed work after the last post and suppresses short quiet turns", () => {
  const at = (seq: number, seconds: number, body: EventBody, from: Principal = { kind: "engine" }) =>
    ({ ...event(seq, body, from), at: new Date(seconds * 1000).toISOString() });
  const start = (seq: number, seconds: number, turn: string) => at(seq, seconds, { type: "turn.started", body: { session: "l", turn, inputs: [] } });
  const end = (seq: number, seconds: number, turn: string) => at(seq, seconds, { type: "turn.ended", body: { session: "l", turn, outcome: "completed", usage } });
  const post = (seq: number, seconds: number) => at(seq, seconds, { type: "post.created", body: { text: "update", home: { kind: "feed" } } }, lead);
  const job = at(7, 80, { type: "shell.job.ended", body: { session: "l", job: "j", exitCode: 0, tail: "" } });
  const rows = feedRows([start(1, 0, "a"), post(2, 10), end(3, 20, "a"),
    start(4, 30, "b"), post(5, 40), end(6, 100, "b"),
    start(8, 101, "c"), job, end(9, 110, "c")], { kind: "feed" });
  assert.deepEqual(rows.map(row => row.kind), ["post", "post", "work"]);
  const work = rows[2];
  assert.equal(work.kind === "work" && workSummary(work.events, id => id), "l worked for 1 min · 1 job");
});

test("a work row opens only when it has more to show than its summary", () => {
  const started = decodeEvent(event(1, { type: "session.started", body: { session: "eval-worker", name: "eval-worker", profile: "trial" } }));
  const dormant = decodeEvent(event(2, { type: "session.closed", body: { session: "scout", reason: "dormant" } }));
  const pr = decodeEvent(event(3, { type: "pr.opened", body: { repo: "dyna", number: 7, session: "builder", url: "https://example.com/7" } }));
  assert.ok(started && dormant && pr);
  assert.equal(workIsBare([started], id => id), true);
  assert.equal(workIsBare([dormant], id => id), true);
  assert.equal(workIsBare([started, started], id => id), false);
  assert.equal(workIsBare([pr], id => id), false);
});

test("keeps dormant closes out of the summary and explains them in detail", () => {
  const quiet = decodeEvent(event(1, { type: "session.closed", body: { session: "scout", reason: "dormant" } }));
  const plain = decodeEvent(event(2, { type: "session.closed", body: { session: "builder" } }));
  assert.ok(quiet && plain);
  assert.equal(workSummary([quiet, plain], id => id), "1 closed");
  assert.equal(describe(quiet, id => id), "scout closed after 7 quiet days");
  const evicted = decodeEvent(event(3, { type: "session.closed", body: { session: "worker", reason: "disk" } }));
  assert.ok(evicted);
  assert.equal(describe(evicted, id => id), "worker closed to free disk");
});

test("splits activity at midnight and caps failures", () => {
  const failure = (seq: number) => event(seq, { type: "turn.ended", body: { session: "l", turn: `t${seq}`, outcome: "failed", usage } });
  const rows = feedRows(Array.from({ length: 7 }, (_, i) => failure(i + 1)), { kind: "feed" });
  assert.deepEqual(rows.map(row => row.kind), ["failed", "failed", "failed", "failed", "failed", "more-failures"]);
  assert.equal(rows[5].kind === "more-failures" && rows[5].failures.length, 2);
  const start = event(8, { type: "turn.started", body: { session: "l", turn: "t", inputs: [] } });
  const events = [{ ...start, at: new Date(2026, 9, 30, 23).toISOString() }, { ...start, seq: 9, at: new Date(2026, 9, 31, 1).toISOString() }];
  assert.deepEqual(dayRows(events, { kind: "feed" }).map(row => row.kind), []);
});

test("a room limit change is one plain line, and zero means no limit", () => {
  const limits = (seq: number, cpus: number, memBytes: number) => event(seq, { type: "room.limits_changed", body: { cpus, memBytes } }, { kind: "owner" });
  const rows = feedRows([limits(1, 8, 16 * 2 ** 30), limits(2, 1, 0), limits(3, 0, 1.5 * 2 ** 30), limits(4, 0, 10 * 2 ** 20), limits(5, 0, 0)], { kind: "feed" });
  assert.deepEqual(rows.flatMap(row => row.kind === "notice" ? [describe(row.event, id => id)] : []),
    ["You set the room limit to 8 CPUs and 16 GB", "You set the room limit to 1 CPU and no memory limit", "You set the room limit to no CPU limit and 1.5 GB",
      "You set the room limit to no CPU limit and 10 MB", "You removed the room limit"]);
  const raw = { seq: 6, id: "6", at: "", room: "r", from: { kind: "owner" }, type: "room.limits_changed" };
  assert.equal(decodeEvent({ ...raw, body: { cpus: 8, memBytes: 0 } })?.type, "room.limits_changed");
  assert.equal(decodeEvent({ ...raw, body: { cpus: -1, memBytes: 0 } }), undefined);
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

test("a post that carried a final reply names the reply's target once", () => {
  const owner: Principal = { kind: "owner" }, parent: Principal = { kind: "session", session: "p" };
  const posted = event(1, { type: "post.created", body: { text: "Report", home: { kind: "feed" }, mentions: [parent] } }, { kind: "session", session: "l" });
  const ended = event(2, { type: "turn.ended", body: { session: "l", turn: "t", outcome: "completed", usage, replies: [
    { reply: { kind: "final", target: owner, home: { kind: "feed" } }, posted: "1" }, { reply: { kind: "final", target: parent, home: { kind: "feed" } }, posted: "1" }] } });
  const row = feedRows([posted, ended], { kind: "feed" })[0];
  assert.deepEqual(row.kind === "post" && row.to, [parent, owner]);
  assert.deepEqual(feedRows([posted], { kind: "feed" }).map(item => item.kind === "post" && item.to), [[parent]]);
});

test("a turn that ended on a post in the feed adds no empty answer to the DM that asked", () => {
  const owner: Principal = { kind: "owner" };
  const posted = event(1, { type: "post.created", body: { text: "Report", home: { kind: "feed" } } }, { kind: "session", session: "l" });
  const ended = event(2, { type: "turn.ended", body: { session: "l", turn: "t", outcome: "completed", usage,
    replies: [{ reply: { kind: "final", target: owner, home: { kind: "dm", session: "l" } }, posted: "1" }] } });
  assert.deepEqual(feedRows([posted, ended], { kind: "dm", session: "l" }).filter(row => row.kind === "post"), []);
  const row = feedRows([posted, ended], { kind: "feed" })[0];
  assert.deepEqual(row.kind === "post" && [row.text, row.to], ["Report", [owner]]);
});

test("puts a turn's final reply in its home only, followed by its work row", () => {
  const ended = event(3, { type: "turn.ended", body: { session: "l", turn: "t", outcome: "completed", final: "Answer", usage,
    replies: [{ reply: { kind: "final", target: { kind: "owner" }, home: { kind: "dm", session: "l" } } }] } });
  const dm = feedRows([ended], { kind: "dm", session: "l" });
  assert.deepEqual(dm.map(row => row.kind === "post" ? row.text : row.kind), ["Answer", "work"]);
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

test("a nearby resume merges with its pause unless a post separates them", () => {
  const owner: Principal = { kind: "owner" };
  const paused = { ...event(1, { type: "room.paused", body: {} }, owner), at: "2026-10-30T12:00:00Z" };
  const resumed = { ...event(3, { type: "room.resumed", body: {} }, owner), at: "2026-10-30T12:04:00Z" };
  const post = { ...event(2, { type: "post.created", body: { text: "hello", home: { kind: "feed" } } }, owner), at: "2026-10-30T12:02:00Z" };
  const joined = feedRows([paused, resumed], { kind: "feed" });
  assert.equal(joined.length, 1);
  assert.equal(joined[0].kind === "notice" && joined[0].resumedAt, resumed.at);
  assert.deepEqual(feedRows([paused, post, resumed], { kind: "feed" }).map(row => row.kind), ["notice", "post", "notice"]);
});

test("a decided request folds into the activity block", () => {
  const card = { title: "asks for host access", target: { kind: "room" as const, name: "r" } };
  const decision = { by: { kind: "owner" as const }, approved: true, at: "" };
  const rows = feedRows([
    event(1, { type: "turn.started", body: { session: "l", turn: "t1", inputs: [] } }),
    event(2, { type: "op.requested", body: { id: "op1", kind: "hostexec.grant", by: lead, class: "owner", card, expires: "" } }, lead),
    event(3, { type: "op.decided", body: { id: "op1", state: "approved", decision } }, { kind: "owner" }),
    event(4, { type: "turn.ended", body: { session: "l", turn: "t1", outcome: "completed", usage } }),
  ], { kind: "feed" });
  assert.deepEqual(rows.map(row => row.kind), ["work"]);
  const work = rows[0];
  assert.equal(work.kind === "work" && workSummary(work.events, id => id), "l worked · 1 approval by you");
  assert.equal(work.kind === "work" && describe(work.events[1], id => id), "l asks for host access · r");
  assert.equal(describe(event(2, { type: "op.requested", body: { id: "op1", kind: "hostexec.grant", by: { kind: "owner" }, class: "owner", card, expires: "" } }), id => id), "You ask for host access · r");
  const retained = dayRows([
    event(1, { type: "turn.started", body: { session: "l", turn: "t1", inputs: [] } }),
    event(2, { type: "op.requested", body: { id: "op1", kind: "hostexec.grant", by: lead, class: "owner", card, expires: "" } }, lead),
    event(3, { type: "op.decided", body: { id: "op1", state: "approved", decision } }, { kind: "owner" }),
    event(4, { type: "turn.ended", body: { session: "l", turn: "t1", outcome: "completed", usage } }),
  ], { kind: "feed" }, [], 0, new Set(["op1"])).filter(row => row.kind !== "day");
  assert.deepEqual(retained.map(row => row.kind), ["work", "request"]);
  assert.equal(retained[1].kind === "request" && retained[1].request.state, "approved");
  assert.deepEqual(feedRows([event(3, { type: "op.decided", body: { id: "op1", state: "approved", decision } }, { kind: "owner" })], { kind: "dm", session: "l" }), []);
});

test("says how long the work in a row took, from the first turn's start to the last turn's end", () => {
  const at = (seq: number, minutes: number, body: EventBody) => ({ ...event(seq, body), at: new Date(minutes * 60_000).toISOString() });
  const ended = (seq: number, minutes: number, turn: string) => at(seq, minutes, { type: "turn.ended", body: { session: "l", turn, outcome: "completed", usage } });
  const events = [
    at(1, 0, { type: "turn.started", body: { session: "l", turn: "t1", inputs: [] } }), ended(2, 2, "t1"),
    at(3, 3, { type: "shell.job.ended", body: { session: "l", job: "j", exitCode: 0, tail: "" } }), ended(4, 6, "t2"),
  ];
  assert.equal(workSummary(events, session => names[session]), "lead-web worked for 6 min · 1 job");
  assert.equal(workSummary([events[0], ended(2, 80, "t1")], session => names[session]), "lead-web worked for 1 h 20 min");
  // A turn that answers here ends in the answer's row, which still gives the work row above it its end.
  const answered = at(2, 4, { type: "turn.ended", body: { session: "l", turn: "t1", outcome: "completed", usage, final: "Done", replies: [{ reply: { kind: "final" } }] } });
  const rows = dayRows([events[0], answered], { kind: "feed" });
  const work = rows.find(row => row.kind === "work");
  assert.equal(work?.kind === "work" && workSummary(work.events, session => names[session], work.until), "lead-web worked for 4 min");
});

test("a long path in an activity label keeps its first folder and its file, and short paths stay whole", () => {
  assert.equal(shortPaths("editing next/engine/approve/store/files.go"), "editing next/…/store/files.go");
  assert.equal(shortPaths("editing next/engine/files.go and web/src/a.ts"), "editing next/engine/files.go and web/src/a.ts");
});

test("a restart warning remains visible beside other work in a session DM", () => {
  const notice = decodeEvent(event(2, { type: "session.restarted", body: { session: "l" } }));
  assert.ok(notice);
  assert.equal(dmOf(notice), "l");
  const started = event(1, { type: "turn.started", body: { session: "l", turn: "t", inputs: [] } });
  assert.deepEqual(feedRows([started, notice], { kind: "dm", session: "l" }, [], 1).map(row => row.kind), ["restarted"]);
});

test("an image update keeps the volume, so it shows no restart warning", () => {
  const notice = decodeEvent(event(2, { type: "session.restarted", body: { session: "l", imageUpdated: true } }));
  assert.ok(notice);
  assert.deepEqual(feedRows([notice], { kind: "dm", session: "l" }, [], 1), []);
});

test("an image that failed to load becomes a file card, and the others stay thumbnails", () => {
  const image = (sha256: string) => ({ name: `${sha256}.png`, media: "image/png", bytes: 10, sha256 });
  const files = [image("a"), image("b"), { name: "notes.txt", media: "text/plain", bytes: 10, sha256: "c" }];
  assert.deepEqual(files.filter(file => isThumb(file, ["b"])).map(file => file.sha256), ["a"]);
});


test("account alerts decode but never enter a room feed", () => {
  const base = { seq: 1, id: "e", at: "2026-10-31T12:00:00Z", from: { kind: "engine" } };
  for (const alert of [
    { type: "account.failed", body: { accountId: "a", reason: "refresh_failed" } },
    { type: "account.quota_low", body: { accountId: "a", window: "week", remainingPercent: 5, floor: 10 } },
  ]) {
    const decoded = decodeEvent({ ...base, ...alert });
    assert.ok(decoded);
    assert.deepEqual(dayRows([decoded], { kind: "feed" }), []);
  }
});

const summary = { repo: "o/r", number: 7, url: "https://github.com/o/r/pull/7", title: "t", author: "a", session: "builder", draft: false, state: "open", head: "h", review: "", ci: "", merge: "" } as const;

test("a PR update with news is a work row that links its PR, and one without stays out of the feed", () => {
  const news = decodeEvent(event(1, { type: "pr.changed", body: { session: "builder", changes: [{ pr: { ...summary, ci: "failure" }, kinds: ["ci_failed", "conflict"], outside: false }] } }));
  const quiet = decodeEvent(event(2, { type: "pr.changed", body: { session: "builder", changes: [{ pr: { ...summary, ci: "pending" }, outside: false }] } }));
  assert.ok(news && quiet);
  assert.equal(describe(news, id => id), "o/r#7 CI failed, conflicts with its base");
  assert.equal(workIsBare([news], id => id), false);
  const rows = feedRows([news, quiet], { kind: "feed" }).filter(row => row.kind === "work");
  assert.equal(rows.length, 1);
  assert.deepEqual(rows[0].kind === "work" && rows[0].events.map(item => item.seq), [1]);
  assert.equal(feedRows([quiet], { kind: "feed" }).filter(row => row.kind === "work").length, 0);
  assert.equal(workSummary([news, news], id => id), "2 pull request updates");
});

test("a malformed PR update is skipped", () => {
  assert.equal(decodeEvent(event(1, { type: "pr.changed", body: { session: "builder", changes: [{ pr: { ...summary, ci: "green" }, outside: false }] } } as unknown as Parameters<typeof event>[1])), undefined);
});

test("a tracked PR's words mark only what needs someone", () => {
  assert.deepEqual(prWords(summary), []);
  assert.deepEqual(prWords({ ...summary, ci: "success", review: "approved" }), [{ label: "CI passed" }, { label: "Approved" }]);
  assert.deepEqual(prWords({ ...summary, ci: "failure", review: "changes_requested", merge: "conflict" }),
    [{ label: "CI failed", tone: "failed" }, { label: "Changes requested", tone: "warning" }, { label: "Conflict", tone: "failed" }]);
  assert.deepEqual(prWords({ ...summary, ci: "failure", state: "merged" }), [{ label: "Merged" }]);
  assert.deepEqual(prWords({ ...summary, state: "closed" }), [{ label: "Closed" }]);
});

test("the events for subscribers stay out of the feed, as the pr.changed beside them shows the news", () => {
  const merged = decodeEvent(event(1, { type: "pr.merged", body: { repo: "o/r", number: 7, session: "builder", url: "u" } }));
  const closed = decodeEvent(event(2, { type: "pr.closed", body: { repo: "o/r", number: 8, session: "builder", url: "u" } }));
  const ci = decodeEvent(event(3, { type: "ci.finished", body: { repo: "o/r", number: 9, session: "builder", url: "u", head: "h", result: "failure" } }));
  const main = decodeEvent(event(4, { type: "repo.main_updated", body: { repo: "o/r", head: "h" } }));
  assert.ok(merged && closed && ci && main);
  assert.equal(subject(main), undefined);
  assert.equal(feedRows([merged, closed, ci, main], { kind: "feed" }).filter(row => row.kind === "work").length, 0);
});
