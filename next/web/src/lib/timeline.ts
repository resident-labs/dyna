// Ported from T3 Code (apps/web/src/components/chat/MessagesTimeline.logic.ts), MIT License, Copyright (c) 2026 T3 Tools Inc. See next/third_party/t3code/LICENSE.
import type { TranscriptInput, TranscriptItem } from "./contract.gen.ts";
import { titleOf, type StepKind } from "./steps.ts";

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

// Ported from T3 Code (packages/client-runtime/src/work-log/presentation.ts), MIT License, Copyright (c) 2026 T3 Tools Inc. See next/third_party/t3code/LICENSE.
// Dyna: Transcript tools have no T3 source or integration metadata.
export function summarizeWork(items: TranscriptItem[]): string {
  const tools = items.filter(item => item.kind === "tool");
  if (!tools.length) return items.length === 1 ? "Thought" : `Thought (×${items.length})`;
  const groups = new Map<StepKind, number>();
  for (const item of tools) {
    const kind = titleOf(item).kind;
    groups.set(kind, (groups.get(kind) ?? 0) + 1);
  }
  const selected = [...groups].map(([kind, count], index) => ({ kind, count, index }))
    .sort((a, b) => Number(b.kind === "run" || b.kind === "edit" || b.kind === "write") - Number(a.kind === "run" || a.kind === "edit" || a.kind === "write") || a.index - b.index)
    .slice(0, 2).sort((a, b) => a.index - b.index);
  const labels = selected.map(({kind, count}) => {
    const plural = count === 1 ? "" : "s";
    if (kind === "run") return `Ran ${count} command${plural}`;
    if (kind === "edit" || kind === "write") return `Changed ${count} file${plural}`;
    if (kind === "read") return `Read ${count} file${plural}`;
    if (kind === "search") return `Searched code ${count} ${count === 1 ? "time" : "times"}`;
    return `Used ${count} tool${plural}`;
  });
  const remaining = tools.length - selected.reduce((sum, group) => sum + group.count, 0);
  if (remaining) labels.push(`Performed ${remaining} other ${remaining === 1 ? "action" : "actions"}`);
  const sentence = labels.map((label, index) => index ? label[0].toLowerCase() + label.slice(1) : label);
  return sentence.length < 3 ? sentence.join(" and ") : `${sentence.slice(0, -1).join(", ")}, and ${sentence.at(-1)}`;
}

// Dyna: Continuations can repeat tools and their posts in a separate page.
export function mergeTimelineItems(existing: TranscriptItem[], incoming: TranscriptItem[]): TranscriptItem[] {
  const byId = new Map(existing.map(item => [item.id, item]));
  for (const item of incoming) byId.set(item.id, item);
  const items = [...byId.values()];
  const nested = new Set(items.flatMap(item => (item.posts ?? []).map(post => post.event || post.id)));
  return items.filter(item => item.kind !== "post" || !nested.has(item.event || item.id)).sort((a, b) => (a.index ?? 0) - (b.index ?? 0));
}

// Dyna: A refresh can race an in-flight input continuation.
export function mergeTimelineInputs(existing: TranscriptInput[], incoming: TranscriptInput[]): TranscriptInput[] {
  return [...new Map([...existing, ...incoming].map(input => [input.id, input])).values()].sort((a, b) => a.seq - b.seq);
}
