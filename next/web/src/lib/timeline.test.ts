// Ported from T3 Code (apps/web/src/components/chat/MessagesTimeline.logic.test.ts), MIT License, Copyright (c) 2026 T3 Tools Inc. See next/third_party/t3code/LICENSE.
import { test } from "node:test";
import assert from "node:assert/strict";
import { deriveTimelineGroups, mergeTimelineItems } from "./timeline.ts";
import type { TranscriptItem } from "./contract.gen.ts";

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

test("continuations restore item order and deduplicate linked posts", () => {
  const post: TranscriptItem = { id: "p", event: "p", turn: "t", at: "", kind: "post", index: 99 };
  const tool: TranscriptItem = { id: "tool", turn: "t", at: "", kind: "tool", index: 2, posts: [post] };
  const text: TranscriptItem = { id: "final", turn: "t", at: "", kind: "text", index: 3, text: "Done" };
  assert.deepEqual(mergeTimelineItems([text, post], [tool]).map(item => item.id), ["tool", "final"]);
});
