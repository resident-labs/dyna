// Ported from T3 Code (apps/web/src/components/chat/MessagesTimeline.logic.test.ts), MIT License, Copyright (c) 2026 T3 Tools Inc. See next/third_party/t3code/LICENSE.
import { test } from "node:test";
import assert from "node:assert/strict";
import { summarizeWork, deriveTimelineGroups, finalTimelineItem, postDestination, queuedTimelinePosts, segmentDuration, steerInput, timelineInput, timelineInputText, timelineSegments, visibleTimelineText, formatTimelineDuration, mergeTimelineInputs, mergeTimelineItems, timelineItemRunning, workLayout } from "./timeline.ts";
import type { TranscriptInput, TranscriptItem, TranscriptTurn } from "./contract.gen.ts";

const steer = (id: string, at = ""): TranscriptItem => ({ id: `steer:${id}`, event: id, seq: 6, turn: "t", at, kind: "steer", from: { kind: "owner" } });
const work = (id: string): TranscriptItem => ({ id, turn: "t", at: "", kind: "tool", tool: "bash" });

test("a steer leaves the queue the moment the transcript holds it, and a message after the turn stays queued", () => {
  const input = { id: "in", seq: 5, at: "", room: "r", type: "post.created", from: { kind: "owner" }, text: "" } as TranscriptInput;
  const turn = (items: TranscriptItem[]): TranscriptTurn => ({ id: "t", started: "", inputs: [input], items, more: false });
  const posts = [{ id: "in", seq: 5 }, { id: "sent", seq: 6 }, { id: "after", seq: 7 }];
  assert.deepEqual(queuedTimelinePosts(posts, [turn([work("a")])]).map(post => post.id), ["sent", "after"]);
  assert.deepEqual(queuedTimelinePosts(posts, [turn([work("a"), steer("sent")])]).map(post => post.id), ["after"]);
});

test("a later turn's input can no longer hide a steer: the post has its place in its turn", () => {
  const items = [work("a"), steer("sent"), { id: "b", turn: "t", at: "", kind: "text", text: "answer" } as TranscriptItem];
  const input = (id: string, seq: number) => ({ id, seq, at: "", room: "r", type: "post.created", from: { kind: "owner" }, text: "" }) as TranscriptInput;
  const turns: TranscriptTurn[] = [{ id: "t", started: "", inputs: [input("in", 5)], items, more: false }, { id: "u", started: "", inputs: [input("next", 9)], items: [], more: false }];
  assert.deepEqual(queuedTimelinePosts([{ id: "sent", seq: 6 }, { id: "next", seq: 9 }], turns), []);
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

// Ported from T3 Code (apps/web/src/components/chat/MessagesTimeline.logic.test.ts), MIT License, Copyright (c) 2026 T3 Tools Inc. See next/third_party/t3code/LICENSE.
test("the top row keeps the whole turn's time, and a steer's row counts from that steer", () => {
  const segments = timelineSegments("t", [work("a"), steer("one", "2026-11-01T00:00:10Z"), steer("two", "2026-11-01T00:00:12Z"), work("b"), steer("three", "2026-11-01T00:00:20Z"), work("c")]);
  const durations = segments.map((_, index) => segmentDuration(segments, index, "2026-11-01T00:00:00Z", "2026-11-01T00:00:25Z"));
  assert.deepEqual(durations, [25_000, 8_000, 5_000]);
});

test("a steer is no answer, and an answer before it folds as working text", () => {
  const before: TranscriptItem = { id: "a", turn: "t", at: "", kind: "text", text: "first" };
  const after: TranscriptItem = { id: "b", turn: "t", at: "", kind: "text", text: "second" };
  assert.equal(finalTimelineItem([before, steer("sent"), after], "s")?.id, "b");
  assert.equal(finalTimelineItem([before, steer("sent")], "s")?.id, "a");
});

test("a repeat read of a turn keeps one steer, in its place", () => {
  const first = work("a"), late = { ...work("b"), index: 2 };
  const placed = { ...steer("sent"), index: 1 };
  assert.deepEqual(mergeTimelineItems([first, placed], [first, placed, late]).map(item => item.id), ["a", "steer:sent", "b"]);
});

test("a post stays queued until the transcript holds it, even after an agent's later post", () => {
  const input = { id: "in", seq: 5, at: "", room: "r", type: "post.created", from: { kind: "owner" }, text: "" } as TranscriptInput;
  const nested: TranscriptItem = { id: "out", event: "out", seq: 7, turn: "t", at: "", kind: "post" };
  const turn: TranscriptTurn = { id: "t", started: "", inputs: [input], items: [{ id: "tool", turn: "t", at: "", kind: "tool", tool: "post", posts: [nested] }], more: false };
  const queued = queuedTimelinePosts([{ id: "old", seq: 3 }, { id: "in", seq: 5 }, { id: "waiting", seq: 6 }, { id: "out", seq: 7 }, { id: "new", seq: 8 }], [turn]);
  assert.deepEqual(queued.map(post => post.id), ["waiting", "new"]);
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
    { tool: "mcp__shell__shell", status: "ok" }, { tool: "read", status: "ok" }, { tool: "read", status: "error" }, { tool: "write", status: "ok" },
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

test("completed text-only turns show every message without a disclosure", () => {
  assert.equal(visibleTimelineText("first", "final", true, false, false), true);
  assert.equal(visibleTimelineText("final", "final", true, false, false), true);
  assert.equal(visibleTimelineText("first", "final", true, true, false), false);
});

const ownedPost = (id: string, at: string): TranscriptItem => ({ id, turn: "t", at, kind: "post", from: { kind: "session", session: "s" }, home: { kind: "feed" }, room: "r" });
const message = (id: string, at: string): TranscriptItem => ({ id, turn: "t", at, kind: "text", text: id });

test("a post that ends the turn is its final message, so earlier text folds", () => {
  const tool: TranscriptItem = { id: "tool", turn: "t", at: "2026-11-01T00:00:01Z", kind: "tool", posts: [ownedPost("post", "2026-11-01T00:00:02Z")] };
  const items = [message("narration", "2026-11-01T00:00:01Z"), tool];
  const final = finalTimelineItem(items, "s")?.id;
  assert.equal(final, "post");
  assert.equal(visibleTimelineText("narration", final, true, true, false), false);
  assert.equal(visibleTimelineText("narration", final, true, true, true), true);
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
  assert.equal(timelineInputText({ ...clipped, type: "session.started" }), "Started work");
  assert.deepEqual(timelineInput({ ...clipped, type: "checkin.due" }), { kind: "line", text: "Event: checkin.due" });
  assert.deepEqual(timelineInput({ ...clipped, more: false }), { kind: "line", text: "Event: turn.ended" });
});

test("an engine notice is one plain line, and a type this build does not know says what it is", () => {
  const children = [{ session: "c", name: "api-lead", state: "idle", idle: 60000, goal: "g", text: "ship", open: 1 }];
  const failure = { to: "root", failures: [{ session: "c", name: "api-lead", cause: "model_failed" }] };
  assert.deepEqual(timelineInput(wake("checkin.due", { session: "s", children })), { kind: "line", text: "Check-in on 1 stalled session" });
  assert.deepEqual(timelineInput(wake("session.failed", failure)), { kind: "failed", text: "api-lead failed" });
  assert.deepEqual(timelineInput(wake("pr.opened", { repo: "r", number: 7, session: "c", url: "https://x/7" })), { kind: "line", text: "PR opened: https://x/7" });
  assert.deepEqual(timelineInput(wake("shell.job.ended", { session: "s", job: "j", exitCode: 0, tail: "", description: "Build" })), { kind: "line", text: "Background job Build ended" });
  assert.deepEqual(timelineInput(wake("hook.due", { subscription: "ci" })), { kind: "line", text: "Subscription ci is due" });
  assert.deepEqual(timelineInput(wake("hook.finished", { subscription: "ci", result: "passed", exitCode: 0 })), { kind: "line", text: "Subscription ci passed, exit code 0" });
  assert.deepEqual(timelineInput(wake("hook.finished", { subscription: "ci", result: "failed", exitCode: 2 })), { kind: "failed", text: "Subscription ci failed, exit code 2" });
  assert.deepEqual(timelineInput(wake("hook.finished", { subscription: "ci", result: "failed", exitCode: -1, cause: "run_timeout" })).kind, "failed");
  assert.ok(!timelineInputText(wake("hook.finished", { subscription: "ci", result: "failed", exitCode: -1, cause: "run_timeout" })).includes("exit code"));
  assert.deepEqual(timelineInput(wake("notice.posted", { text: "The room resumes at noon." })), { kind: "line", text: "The room resumes at noon." });
  assert.deepEqual(timelineInput(wake("future.kind", { any: "shape" })), { kind: "line", text: "Event: future.kind" });
  assert.deepEqual(timelineInput(wake("hook.finished", { result: "unexpected" })), { kind: "line", text: "Event: hook.finished" });
});

test("a brief still reads as the start of the work", () => {
  const started = { session: "s", name: "n", profile: "p" };
  assert.deepEqual(timelineInput(wake("session.started", { ...started, brief: "Do it" })), { kind: "message", text: "Do it" });
  assert.deepEqual(timelineInput(wake("session.started", started)), { kind: "message", text: "Started work" });
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
