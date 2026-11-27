// Ported from T3 Code (apps/web/src/components/chat/MessagesTimeline.logic.test.ts), MIT License, Copyright (c) 2026 T3 Tools Inc. See next/third_party/t3code/LICENSE.
import { test } from "node:test";
import assert from "node:assert/strict";
import { workingLine, compactionNote, summarizeWork, deriveTimelineGroups, finalTimelineItem, postDestination, timelineTailPosts, steerInput, timelineInput, timelineInputText, timelineSegments, formatTimelineDuration, mergeTimelineInputs, mergeTimelineItems, timelineItemRunning, withFinished, workFoldLabel, workLayout, foldInputRuns, engineEventsNote, attemptFailedNote } from "./timeline.ts";
import type { TranscriptInput, TranscriptItem, TranscriptTurn } from "./contract.gen.ts";

const steer = (id: string, at = ""): TranscriptItem => ({ id: `steer:${id}`, event: id, seq: 6, turn: "t", at, kind: "steer", from: { kind: "owner" } });
const work = (id: string): TranscriptItem => ({ id, turn: "t", at: "", kind: "tool", tool: "bash" });

const postAt = (id: string, seq: number, second = seq) => ({ id, seq, at: new Date(second * 1000).toISOString() });
const startAt = postAt("", 0, 5).at;
const tail = (posts: ReturnType<typeof postAt>[], turns: TranscriptTurn[] | null | undefined, liveTurn?: string) =>
  timelineTailPosts(posts, turns, liveTurn).map(({ post, queued }) => [post.id, queued]);

test("DM history waits for the first transcript page, but a loaded empty transcript can have a waiting post", () => {
  const posts = [postAt("first", 1)];
  assert.deepEqual(tail(posts, undefined), []);
  assert.deepEqual(tail(posts, []), [["first", true]]);
});

test("a failed first transcript read leaves existing and newly confirmed DM posts readable without queue notes", () => {
  assert.deepEqual(tail([postAt("old", 1)], null, "t"), [["old", false]]);
  assert.deepEqual(tail([postAt("old", 1), postAt("sent", 2)], null, "t"), [["old", false], ["sent", false]]);
});

test("an unmatched post predating the newest loaded turn is not queued", () => {
  const turn: TranscriptTurn = { id: "t", started: startAt, inputs: [], items: [], more: false };
  const posts = [postAt("old", 4), postAt("same millisecond", 5), postAt("next", 6)];
  assert.deepEqual(tail(posts, [turn]), [["old", false], ["same millisecond", false], ["next", true]]);
  assert.deepEqual(tail(posts, [turn], "newer"), [["old", false], ["same millisecond", false], ["next", false]]);
});

test("a steer leaves the queue the moment the transcript holds it, and the next message stays queued", () => {
  const input: TranscriptInput = { ...postAt("in", 5, 4), room: "r", type: "post.created", from: { kind: "owner" }, text: "" };
  const turn: TranscriptTurn = { id: "t", started: startAt, inputs: [input], items: [work("a")], more: false };
  const posts = [input, postAt("sent", 6), postAt("after", 7)];
  assert.deepEqual(tail(posts, [turn], "t"), [["sent", true], ["after", true]]);
  assert.deepEqual(tail(posts, [{ ...turn, mandatoryItems: true }], "t"), [["sent", false], ["after", false]]);
  assert.deepEqual(tail(posts, [{ ...turn, items: [work("a"), steer("sent")] }], "t"), [["after", true]]);
});

test("a later turn's input can no longer hide a steer: the post has its place in its turn", () => {
  const items = [work("a"), steer("sent"), { id: "b", turn: "t", at: "", kind: "text", text: "answer" } as TranscriptItem];
  const input = (id: string, seq: number) => ({ id, seq, at: "", room: "r", type: "post.created", from: { kind: "owner" }, text: "" }) as TranscriptInput;
  const turns: TranscriptTurn[] = [{ id: "t", started: "", inputs: [input("in", 5)], items, more: false }, { id: "u", started: "", inputs: [input("next", 9)], items: [], more: false }];
  assert.deepEqual(tail([postAt("sent", 6), postAt("next", 9)], turns), []);
  assert.deepEqual(timelineSegments("t", items).map(segment => [segment.steers.map(item => item.id), segment.items.map(item => item.id)]), [[[], ["a"]], [["steer:sent"], ["b"]]]);
});

// Ported from T3 Code (apps/web/src/components/chat/MessagesTimeline.logic.test.ts), MIT License, Copyright (c) 2026 T3 Tools Inc. See next/third_party/t3code/LICENSE.
test("a turn's work splits at each steer, and back-to-back steers split once", () => {
  const items = [work("a"), steer("one"), steer("two"), work("b"), steer("three"), work("c")];
  assert.deepEqual(timelineSegments("t", items).map(segment => [segment.id, segment.steers.map(item => item.id), segment.items.map(item => item.id)]), [
    ["t", [], ["a"]],
    ["steer:one", ["steer:one", "steer:two"], ["b"]],
    ["steer:three", ["steer:three"], ["c"]],
  ]);
  assert.deepEqual(timelineSegments("t", [steer("first"), work("a")]).map(segment => [segment.steers.length, segment.items.length]), [[0, 0], [1, 1]]);
  assert.deepEqual(timelineSegments("t", [work("a")]).map(segment => segment.id), ["t"]);
});

test("a steer is no answer, and the turn's last message comes after it", () => {
  const before: TranscriptItem = { id: "a", turn: "t", at: "", kind: "text", text: "first" };
  const after: TranscriptItem = { id: "b", turn: "t", at: "", kind: "text", text: "second" };
  assert.equal(finalTimelineItem([before, steer("sent"), after], "s")?.id, "b");
  assert.equal(finalTimelineItem([before, steer("sent")], "s")?.id, "a");
});

const thinking = (id: string, at = "", ended?: string): TranscriptItem => ({ id, turn: "t", at, ended, kind: "thinking", text: id });
const text = (id: string): TranscriptItem => ({ id, turn: "t", at: "", kind: "text", text: id });
const toolAt = (id: string, at: string, ended?: string): TranscriptItem => ({ id, turn: "t", at, ended, kind: "tool", tool: "bash" });
const groupIds = (items: TranscriptItem[]) => deriveTimelineGroups(items).map(group => group.kind === "work" ? group.items.map(item => item.id).join("+") : group.id);

test("interleaved narration and work stay in their real order, with each run of work as its own group", () => {
  const items = [thinking("plan"), work("a"), work("b"), text("narration"), work("c"), text("answer"), steer("sent"), work("d"), text("later"), work("e"), steer("again"), text("last")];
  assert.deepEqual(timelineSegments("t", items).map(segment => groupIds(segment.items)), [
    ["plan+a+b", "narration", "c", "answer"],
    ["d", "later", "e"],
    ["last"],
  ]);
});

test("workFoldLabel counts tools and uses wall-clock span, or counts thoughts when there are no tools", () => {
  assert.equal(workFoldLabel([
    thinking("p", "2026-11-01T00:00:00Z"),
    toolAt("a", "2026-11-01T00:00:02Z", "2026-11-01T00:00:10Z"),
    toolAt("b", "2026-11-01T00:00:05Z", "2026-11-01T00:00:42Z"),
    toolAt("c", "2026-11-01T00:00:05Z", "2026-11-01T00:00:20Z"),
  ]), "3 tool calls · 42s");
  assert.equal(workFoldLabel([toolAt("a", "2026-11-01T00:00:00Z", "2026-11-01T00:00:03.200Z")]), "1 tool call · 3.2s");
  assert.equal(workFoldLabel([toolAt("a", "2026-11-01T00:00:00Z", "2026-11-01T00:01:14Z"), toolAt("b", "2026-11-01T00:00:30Z", "2026-11-01T00:01:14Z")]), "2 tool calls · 1m 14s");
  assert.equal(workFoldLabel([thinking("p", "2026-11-01T00:00:00Z", "2026-11-01T00:00:04Z")]), "1 thought · 4s");
  assert.equal(workFoldLabel([thinking("p1", "2026-11-01T00:00:00Z"), thinking("p2", "2026-11-01T00:00:00Z")]), "2 thoughts");
  assert.equal(workFoldLabel([toolAt("a", "")]), "1 tool call");
});

test("a repeat read of a turn keeps one steer, in its place", () => {
  const first = work("a"), late = { ...work("b"), index: 2 };
  const placed = { ...steer("sent"), index: 1 };
  assert.deepEqual(mergeTimelineItems([first, placed], [first, placed, late]).map(item => item.id), ["a", "steer:sent", "b"]);
});

test("a post stays queued until the transcript holds it, even after an agent's later post", () => {
  const input = { id: "in", seq: 5, at: "", room: "r", type: "post.created", from: { kind: "owner" }, text: "" } as TranscriptInput;
  const nested: TranscriptItem = { id: "out", event: "out", seq: 7, turn: "t", at: "", kind: "post" };
  const turn: TranscriptTurn = { id: "t", started: startAt, inputs: [input], items: [{ id: "tool", turn: "t", at: "", kind: "tool", tool: "post", posts: [nested] }], more: false };
  assert.deepEqual(tail([postAt("old", 3), postAt("in", 5), postAt("waiting", 6), postAt("out", 7), postAt("new", 8)], [turn]), [["waiting", true], ["new", true]]);
});

test("one item of work has no heading to open first, and a script keeps the heading that counts its actions", () => {
  const thought: TranscriptItem = { id: "a", turn: "t", at: "", kind: "thinking", text: "Plan" };
  const tool: TranscriptItem = { id: "b", turn: "t", at: "", kind: "tool", tool: "bash" };
  const script: TranscriptItem = { id: "c", turn: "t", at: "", kind: "tool", tool: "codemode" };
  assert.equal(workLayout([thought]), "single");
  assert.equal(workLayout([tool]), "single");
  assert.equal(workLayout([script]), "mixed");
  assert.equal(workLayout([thought, { ...thought, id: "z" }]), "thoughts");
  assert.equal(workLayout([thought, tool]), "mixed");
  assert.equal(workLayout([tool, { ...tool, id: "z" }]), "mixed");
});

test("thoughts alone count as thoughts, with no title beside the count", () => {
  const thought: TranscriptItem = { id: "a", turn: "t", at: "", kind: "thinking", text: "Plan" };
  assert.equal(summarizeWork([thought, { ...thought, id: "b" }, { ...thought, id: "c" }]), "3 thoughts");
  assert.equal(summarizeWork([thought, { id: "d", turn: "t", at: "", kind: "tool", tool: "bash" }]), "Ran 1 command");
});

test("summarizes nested actions rather than their script", () => {
  const items: TranscriptItem[] = [{ id: "c", turn: "t", at: "", kind: "tool", tool: "codemode", calls: [
    { tool: "bash", status: "ok" }, { tool: "read", status: "ok" }, { tool: "read", status: "error" }, { tool: "write", status: "ok" },
  ] }];
  assert.equal(summarizeWork(items), "Ran 1 command, read 2 files, and wrote 1 file");
  assert.equal(summarizeWork([{ id: "l", turn: "t", at: "", kind: "tool", tool: "ls" }, { id: "s", turn: "t", at: "", kind: "tool", tool: "codemode" }]), "Used 1 tool and ran 1 script");
  assert.equal(summarizeWork([{ id: "l", turn: "t", at: "", kind: "tool", tool: "ls" }, { id: "f", turn: "t", at: "", kind: "tool", tool: "web_fetch" }]), "Used 2 tools");
});

// Dyna: Transcript items embed posts on their tool call.
test("work stays together until a visible message separates it", () => {
  const items: TranscriptItem[] = [
    { id: "a", turn: "t", at: "", kind: "thinking", text: "Plan" },
    { id: "b", turn: "t", at: "", kind: "tool", tool: "bash" },
    { id: "c", turn: "t", at: "", kind: "text", text: "Result" },
    { id: "d", turn: "t", at: "", kind: "tool", tool: "read" },
  ];
  assert.deepEqual(deriveTimelineGroups(items).map(group => [group.kind, group.id]), [
    ["work", "a"], ["item", "c"], ["work", "d"],
  ]);
});

test("a thought is finished once it appears, and a tool runs until its result", () => {
  const thought: TranscriptItem = { id: "a", turn: "t", at: "", kind: "thinking", text: "Plan" };
  const tool: TranscriptItem = { id: "b", turn: "t", at: "", kind: "tool", tool: "bash" };
  assert.equal(timelineItemRunning(thought, false), false);
  assert.equal(timelineItemRunning(tool, false), true);
  assert.equal(timelineItemRunning({ ...tool, ended: "2026-10-31T00:00:00Z" }, false), false);
  assert.equal(timelineItemRunning(tool, true), false);
});

test("continuations restore item order and deduplicate linked posts", () => {
  const post: TranscriptItem = { id: "p", event: "p", turn: "t", at: "", kind: "post", index: 99 };
  const tool: TranscriptItem = { id: "tool", turn: "t", at: "", kind: "tool", index: 2, posts: [post] };
  const text: TranscriptItem = { id: "final", turn: "t", at: "", kind: "text", index: 3, text: "Done" };
  assert.deepEqual(mergeTimelineItems([text, post], [tool]).map(item => item.id), ["tool", "final"]);
});

test("refresh retains inputs while a later page is in flight", () => {
  const input = (seq: number): TranscriptInput => ({ id: `${seq}`, seq, at: "", room: "room", type: "post.created", from: { kind: "owner" }, text: `${seq}` });
  const first = [input(1), input(2)];
  const second = mergeTimelineInputs(first, [input(3), input(4)]);
  const refreshed = mergeTimelineInputs(second, first);
  assert.deepEqual(mergeTimelineInputs(refreshed, [input(5)]).map(item => item.seq), [1, 2, 3, 4, 5]);
});

test("T3 durations choose units after rounding", () => {
  assert.equal(formatTimelineDuration(114_000), "1m 54s");
  assert.equal(formatTimelineDuration(120_000), "2m");
  assert.equal(formatTimelineDuration(59_500), "1m");
});

test("a text-only turn has no work groups", () => {
  assert.deepEqual(groupIds([text("first"), text("final")]), ["first", "final"]);
});

const ownedPost = (id: string, at: string): TranscriptItem => ({ id, turn: "t", at, kind: "post", from: { kind: "session", session: "s" }, home: { kind: "feed" }, room: "r" });
const message = (id: string, at: string): TranscriptItem => ({ id, turn: "t", at, kind: "text", text: id });

test("a post that ends the turn is its final message, and the text and work before it stay in order", () => {
  const tool: TranscriptItem = { id: "tool", turn: "t", at: "2026-11-01T00:00:01Z", kind: "tool", posts: [ownedPost("post", "2026-11-01T00:00:02Z")] };
  const items = [message("narration", "2026-11-01T00:00:01Z"), tool];
  assert.equal(finalTimelineItem(items, "s")?.id, "post");
  assert.deepEqual(groupIds(items), ["narration", "tool", "posts:tool"]);
});

test("text after a mid-turn post stays the final message, also when a shell post sorts last", () => {
  const tool: TranscriptItem = { id: "tool", turn: "t", at: "2026-11-01T00:00:01Z", kind: "tool", posts: [ownedPost("nested", "2026-11-01T00:00:01Z")] };
  assert.equal(finalTimelineItem([tool, message("answer", "2026-11-01T00:00:03Z")], "s")?.id, "answer");
  assert.equal(finalTimelineItem([message("answer", "2026-11-01T00:00:03Z"), ownedPost("shell", "2026-11-01T00:00:02Z")], "s")?.id, "answer");
});

test("another session's post is never a turn's final message", () => {
  const other: TranscriptItem = { ...ownedPost("other", "2026-11-01T00:00:02Z"), from: { kind: "session", session: "other" } };
  assert.equal(finalTimelineItem([message("answer", "2026-11-01T00:00:01Z"), other], "s")?.id, "answer");
  assert.equal(finalTimelineItem([{ id: "tool", turn: "t", at: "", kind: "tool" }], "s"), undefined);
  // A slow box clock can date the final text before the post that came first, and the order still decides.
  const earlier: TranscriptItem = { id: "tool", turn: "t", at: "2026-11-01T00:00:01Z", kind: "tool", posts: [ownedPost("nested", "2026-11-01T00:00:09Z")] };
  assert.equal(finalTimelineItem([earlier, message("answer", "2026-11-01T00:00:05Z")], "s")?.id, "answer");
});

test("a post names its destination only when it left this DM", () => {
  const name = (id: string) => `@${id}`;
  const dm = (session: string): TranscriptItem => ({ ...ownedPost("p", ""), home: { kind: "dm", session } });
  assert.equal(postDestination(dm("s"), "s", "r", "general", name), undefined);
  assert.equal(postDestination(dm("other"), "s", "r", "general", name), "@other");
  assert.equal(postDestination(ownedPost("p", ""), "s", "r", "general", name), "#general");
  assert.equal(postDestination({ ...ownedPost("p", ""), mentions: ["a", "b"] }, "s", "r", "general", name), "@a, @b in #general");
  assert.equal(postDestination({ ...ownedPost("p", ""), room: "elsewhere" }, "s", "r", "general", name), "another room");
  assert.equal(postDestination({ ...ownedPost("p", ""), home: undefined }, "s", "r", "general", name), undefined);
});

const usage = { model: "m", inputTokens: 0, outputTokens: 0, cacheReadTokens: 0, cacheWriteTokens: 0 };
const wake = (type: string, body: unknown, from: TranscriptInput["from"] = { kind: "engine" }): TranscriptInput => ({ at: "2026-11-01T00:00:00Z", id: "e", seq: 1, room: "r", type, from, text: JSON.stringify(body) });

test("a child's reply reads as what it said, and never as started work", () => {
  const child = { kind: "session", session: "c" } as const;
  const ended = { session: "c", turn: "t", usage };
  assert.deepEqual(timelineInput(wake("turn.ended", { ...ended, outcome: "completed", final: "The API is merged." }, child)), { kind: "message", text: "The API is merged." });
  assert.deepEqual(timelineInput(wake("turn.ended", { ...ended, outcome: "completed" }, child)), { kind: "line", text: "Finished with no reply" });
  assert.deepEqual(timelineInput(wake("turn.ended", { ...ended, outcome: "stopped" }, child)), { kind: "line", text: "Stopped with no reply" });
  assert.deepEqual(timelineInput(wake("turn.ended", { ...ended, outcome: "failed", cause: "model_failed", error: "raw" }, child)), { kind: "failed", text: "Turn failed: The model provider refused or failed the request" });
  assert.deepEqual(timelineInput(wake("turn.ended", { ...ended, outcome: "failed", error: "box died" }, child)), { kind: "failed", text: "Turn failed: box died" });
});

test("a long reply waits for its whole body, and an unreadable body stays neutral", () => {
  const body = { session: "c", turn: "t", usage, outcome: "completed", final: "x".repeat(600) };
  const whole = JSON.stringify(body);
  const clipped: TranscriptInput = { ...wake("turn.ended", body), text: whole.slice(0, 512), more: true };
  assert.deepEqual(timelineInput(clipped), { kind: "message", text: "" });
  assert.deepEqual(timelineInput(clipped, whole), { kind: "message", text: body.final });
  assert.deepEqual(timelineInput({ ...clipped, type: "session.started" }), { kind: "line", text: "Session started" });
  assert.deepEqual(timelineInput({ ...clipped, type: "checkin.due" }), { kind: "line", text: "Event: checkin.due" });
  assert.deepEqual(timelineInput({ ...clipped, more: false }), { kind: "line", text: "Event: turn.ended" });
});

test("an engine notice is one plain line, and a type this build does not know says what it is", () => {
  const children = [{ session: "c", name: "api-lead", state: "idle", idle: 60000, goal: "g", text: "ship", open: 1 }];
  const failure = { to: "root", failures: [{ session: "c", name: "api-lead", cause: "model_failed" }] };
  assert.deepEqual(timelineInput(wake("checkin.due", { session: "s", children })), { kind: "line", text: "Check-in on 1 stalled session" });
  assert.deepEqual(timelineInput(wake("session.failed", failure)), { kind: "failed", text: "api-lead failed" });
  assert.deepEqual(timelineInput(wake("github.pr.opened", { repo: "r", number: 7, session: "c", url: "https://x/7" })), { kind: "line", text: "PR opened: https://x/7" });
  assert.deepEqual(timelineInput(wake("shell.job.ended", { session: "s", job: "j", end: { kind: "exit" }, tail: "", description: "Build" })), { kind: "line", text: "Background job Build ended" });
  assert.deepEqual(timelineInput(wake("hook.due", { subscription: "ci" })), { kind: "line", text: "Subscription ci is due" });
  assert.deepEqual(timelineInput(wake("hook.finished", { subscription: "ci", result: "passed", exitCode: 0 })), { kind: "line", text: "Subscription ci passed, exit code 0" });
  assert.deepEqual(timelineInput(wake("hook.finished", { subscription: "ci", result: "failed", exitCode: 2 })), { kind: "failed", text: "Subscription ci failed, exit code 2" });
  assert.deepEqual(timelineInput(wake("hook.finished", { subscription: "ci", result: "failed", exitCode: -1, cause: "run_timeout" })).kind, "failed");
  assert.ok(!timelineInputText(wake("hook.finished", { subscription: "ci", result: "failed", exitCode: -1, cause: "run_timeout" })).includes("exit code"));
  assert.deepEqual(timelineInput(wake("notice.posted", { text: "The room resumes at noon." })), { kind: "line", text: "The room resumes at noon." });
  assert.deepEqual(timelineInput(wake("session.notice", { session: "ses_a", text: "package npm:x@1 did not load" })), { kind: "line", text: "package npm:x@1 did not load" });
  assert.deepEqual(timelineInput(wake("future.kind", { any: "shape" })), { kind: "line", text: "Event: future.kind" });
  assert.deepEqual(timelineInput(wake("hook.finished", { result: "unexpected" })), { kind: "line", text: "Event: hook.finished" });
});

test("a brief still reads as the start of the work", () => {
  const started = { session: "s", name: "n", profile: "p" };
  assert.deepEqual(timelineInput(wake("session.started", { ...started, brief: "Do it" })), { kind: "message", text: "Do it" });
  assert.deepEqual(timelineInput(wake("session.started", started)), { kind: "line", text: "Session started" });
  assert.deepEqual(timelineInput(wake("session.started", { ...started, brief: "" })), { kind: "line", text: "Session started" });
});

test("a steered reply decodes as an input does", () => {
  const body = { session: "c", turn: "t", usage, outcome: "completed", final: "Done." };
  const item: TranscriptItem = { id: "steer:9", event: "e", seq: 9, room: "r", turn: "t", at: "2026-11-01T00:00:00Z", kind: "steer", type: "turn.ended", from: { kind: "session", session: "c" }, text: JSON.stringify(body) };
  assert.deepEqual(timelineInput(steerInput(item)), { kind: "message", text: "Done." });
  assert.deepEqual(timelineInput(steerInput({ ...item, type: "post.created", text: "words" })), { kind: "message", text: "words" });
});

test("an answered question reads in the thread as its text", () => {
  const input = { at: "", id: "e", seq: 1, room: "r", type: "question.answered", from: { kind: "owner" }, text: "Question: Which?" } as const;
  assert.equal(timelineInputText(input), "Question: Which?");
  assert.equal(timelineInputText({ ...input, more: true }, "Question: Which?\n\nAnswer: This"), "Question: Which?\n\nAnswer: This");
});

test("a compaction is one line that says how large the conversation was", () => {
  assert.equal(compactionNote({ tokens: 153400 }), "Compacted the conversation at 153k tokens");
  assert.equal(compactionNote({}), "Compacted the conversation");
  const item = { id: "c", turn: "t", kind: "compaction", at: "2026-11-01T00:00:00Z" } as const;
  assert.deepEqual(deriveTimelineGroups([item]), [{ kind: "item", id: "c", item }]);
});

test("a running turn's working line says it is compacting while its live activity is the compaction", () => {
  const started = "2026-11-01T00:00:00Z";
  const now = Date.parse(started) + 109_000;
  assert.equal(workingLine(started, now, "compaction"), `Compacting the conversation… ${formatTimelineDuration(109_000)}`);
  assert.equal(workingLine(started, now, "model_wait"), `waiting for model · ${formatTimelineDuration(109_000)}`);
  assert.equal(workingLine(started, now, "tool"), `Working for ${formatTimelineDuration(109_000)}`);
  assert.equal(workingLine(started, now), `Working for ${formatTimelineDuration(109_000)}`);
  assert.equal(compactionNote({ tokens: 153400 }), "Compacted the conversation at 153k tokens");
});

test("live working time waits a second and counts whole elapsed seconds", () => {
  const started = "2026-11-01T00:00:00Z";
  for (const [elapsed, expected] of [[0, "Working…"], [32, "Working…"], [999, "Working…"], [1000, "Working for 1s"], [1100, "Working for 1s"], [6400, "Working for 6s"], [12900, "Working for 12s"], [59999, "Working for 59s"], [60000, "Working for 1m"]] as const) {
    assert.equal(workingLine(started, Date.parse(started) + elapsed), expected);
  }
  assert.equal(formatTimelineDuration(6200), "6.2s");
});

test("a post stands before the item that began after it, and posts of one index go in the order sent", () => {
  const tool = (id: string, index: number): TranscriptItem => ({ id, turn: "t", at: "", kind: "tool", index });
  const post = (id: string, index: number, seq: number): TranscriptItem => ({ id, event: id, seq, turn: "t", at: "", kind: "post", index });
  const items = [post("late", 4, 9), tool("c", 3), post("b2", 1, 6), tool("a", 0), post("b1", 1, 5), tool("b", 1), post("mid", 3, 8)];
  assert.deepEqual(mergeTimelineItems([], items).map(item => item.id), ["a", "b1", "b2", "b", "mid", "c", "late"]);
  assert.deepEqual(mergeTimelineItems(items.slice(0, 3), items.slice(3)).map(item => item.id), ["a", "b1", "b2", "b", "mid", "c", "late"]);
});

const github = (id: string, at: string, text = "{}"): TranscriptInput => ({ id, seq: Number(id.replace(/\D/g, "")), at, room: "r", type: "github.pr.changed", from: { kind: "engine" }, text });
const owner = (id: string, text: string): TranscriptInput => ({ id, seq: Number(id.replace(/\D/g, "")), at: "2026-11-01T10:00:00Z", room: "r", type: "post.created", from: { kind: "owner" }, text });

test("a run of engine events folds into one row with its count and span, and a single one stays a line", () => {
  const inputs = [owner("a1", "start"), github("e2", "2026-11-01T09:00:00Z"), { ...github("e3", "2026-11-01T09:30:00Z"), type: "github.ci.finished" }, github("e4", "2026-11-01T10:15:00Z"), owner("a5", "review please"), github("e6", "2026-11-01T11:00:00Z")];
  const runs = foldInputRuns(inputs);
  assert.deepEqual(runs.map(run => run.kind === "input" ? run.input.id : run.count), ["a1", 3, "a5", "e6"]);
  const folded = runs[1];
  assert.ok(folded.kind === "events");
  assert.equal(folded.from, "2026-11-01T09:00:00Z");
  assert.equal(folded.to, "2026-11-01T10:15:00Z");
  assert.match(engineEventsNote(folded), /^3 engine events, /);
});

test("engine events are told by their type and never by their text", () => {
  const talk = owner("a1", "github.pr.changed github.pr.changed");
  assert.deepEqual(foldInputRuns([talk, { ...talk, id: "a2", seq: 2 }]).map(run => run.kind), ["input", "input"]);
  assert.equal(foldInputRuns([{ ...github("e1", ""), type: "git.main_updated" }, { ...github("e2", ""), type: "github.mention" }])[0].kind, "events");
  assert.deepEqual(foldInputRuns([github("e1", ""), { ...github("e2", ""), type: "turn.ended" }]).map(run => run.kind), ["input", "input"]);
});

test("a failed attempt is one muted line that names the failure when Pi did", () => {
  assert.equal(attemptFailedNote({ errorName: "SocketError" }), "Attempt failed (SocketError), retried");
  assert.equal(attemptFailedNote({}), "Attempt failed, retried");
});

test("a call that ended early stops running with its duration, while its batch waits for the others", () => {
  const call = (id: string): TranscriptItem => ({ id, turn: "t", at: "2026-11-14T10:00:00.000Z", kind: "tool", tool: "bash" });
  const items = [call("a"), call("b")];
  const shown = withFinished(items, new Map([["a", { call: "a", durationMs: 8, failed: true }]]));
  assert.deepEqual(shown.map(item => timelineItemRunning(item, false)), [false, true]);
  assert.equal(shown[0].ended, "2026-11-14T10:00:00.008Z");
  assert.equal(shown[0].failed, true);
  assert.equal(shown[1], items[1]);
  // Nothing finished, or a result that came: the items are the transcript's own.
  assert.equal(withFinished(items), items);
  const landed = [{ ...items[0], ended: "2026-11-14T10:00:01.000Z" }];
  assert.equal(withFinished(landed, new Map([["a", { call: "a", durationMs: 8 }]]))[0], landed[0]);
});

test("an advisor call that has its result is followed by the answer between work groups", () => {
  const call = (id: string, patch: Partial<TranscriptItem> = {}): TranscriptItem => ({ id, turn: "t", at: "", kind: "tool", tool: "mcp__dyna__advisor", ended: "2026-01-01T00:00:01Z", output: '{"answer":"Use the cache."}', ...patch });
  const items: TranscriptItem[] = [{ id: "r", turn: "t", at: "", kind: "tool", tool: "read" }, call("a"), { id: "b", turn: "t", at: "", kind: "tool", tool: "bash" }, call("running", { ended: undefined, output: undefined })];
  assert.deepEqual(deriveTimelineGroups(items).map(group => [group.kind, group.id]), [["work", "r"], ["advice", "advice:a"], ["work", "b"]]);
});

test("service.message decodes as an outside feed input with service, author, markdown text and optional data", () => {
  const input: TranscriptInput = {
    id: "ev_1", seq: 10, at: "2026-11-23T01:00:00Z", room: "r", type: "service.message", from: { kind: "engine" },
    text: JSON.stringify({ service: "agent-chat", id: "m1", author: "@x/y", text: "Check **worker-2**", data: { severity: "high" } }),
  };
  assert.deepEqual(timelineInput(input), {
    kind: "feed",
    text: "Check **worker-2**",
    feed: { service: "agent-chat", author: "@x/y", data: '{"severity":"high"}' },
  });
  assert.equal(timelineInputText(input), "agent-chat · @x/y: Check **worker-2**");
  assert.deepEqual(timelineInput({ ...input, text: "{", more: true }), { kind: "line", text: "Loading feed message…" });
  assert.deepEqual(timelineInput({ ...input, text: "{", more: true }, input.text), {
    kind: "feed",
    text: "Check **worker-2**",
    feed: { service: "agent-chat", author: "@x/y", data: '{"severity":"high"}' },
  });
});

test("a service.sent item attached to a tool call follows its work group", () => {
  const sent: TranscriptItem = { id: "ev_2", event: "ev_2", seq: 11, turn: "t", at: "2026-11-23T01:01:00Z", kind: "post", type: "service.sent", from: { kind: "session", session: "s" }, service: "agent-chat", to: "@x/y", text: "On it" };
  const items: TranscriptItem[] = [{ id: "call_1", turn: "t", at: "2026-11-23T01:01:00Z", kind: "tool", tool: "mcp__dyna__service", posts: [sent] }];
  assert.deepEqual(deriveTimelineGroups(items).map(group => group.kind), ["work", "posts"]);
  assert.equal(finalTimelineItem(items, "s")?.id, "ev_2");
});
