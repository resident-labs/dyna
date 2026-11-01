// Ported from T3 Code (apps/web/src/components/chat/MessagesTimeline.logic.test.ts), MIT License, Copyright (c) 2026 T3 Tools Inc. See next/third_party/t3code/LICENSE.
import { test } from "node:test";
import assert from "node:assert/strict";
import { deriveTimelineGroups, finalTimelineItem, postDestination, visibleTimelineText, formatTimelineDuration, mergeTimelineInputs, mergeTimelineItems, timelineItemRunning } from "./timeline.ts";
import type { TranscriptInput, TranscriptItem } from "./contract.gen.ts";

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
