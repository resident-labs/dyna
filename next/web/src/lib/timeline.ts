// Ported from T3 Code (apps/web/src/components/chat/MessagesTimeline.logic.ts), MIT License, Copyright (c) 2026 T3 Tools Inc. See next/third_party/t3code/LICENSE.
import type { TranscriptItem } from "./contract.gen.ts";

// Dyna: Transcript items already arrive in timeline order, and posts can belong to a tool.
export type TimelineGroup = { kind: "work"; id: string; items: TranscriptItem[] } | { kind: "item" | "posts"; id: string; item: TranscriptItem };
export function deriveTimelineGroups(items: TranscriptItem[]): TimelineGroup[] {
  const result: TimelineGroup[] = [];
  for (const item of items) {
    const last = result.at(-1);
    if (item.kind === "tool" || item.kind === "thinking") {
      if (last?.kind === "work") last.items.push(item);
      else result.push({ kind: "work", id: item.id, items: [item] });
    } else result.push({ kind: "item", id: item.id, item });
    if (item.kind === "tool" && ((item.posts?.length ?? 0) > 0 || item.postsOmitted)) {
      result.push({ kind: "posts", id: "posts:" + item.id, item });
    }
  }
  return result;
}
