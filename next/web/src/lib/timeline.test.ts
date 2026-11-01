// Ported from T3 Code (apps/web/src/components/chat/MessagesTimeline.logic.test.ts), MIT License, Copyright (c) 2026 T3 Tools Inc. See next/third_party/t3code/LICENSE.
import { test } from "node:test";
import assert from "node:assert/strict";
import { deriveTimelineGroups, visibleTimelineText, formatTimelineDuration, mergeTimelineInputs, mergeTimelineItems, timelineItemRunning } from "./timeline.ts";
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
