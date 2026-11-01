// Ported from T3 Code (apps/web/src/components/chat/MessagesTimeline.logic.ts), MIT License, Copyright (c) 2026 T3 Tools Inc. See next/third_party/t3code/LICENSE.
import type { TranscriptInput, TranscriptItem, TranscriptTurn } from "./contract.gen.ts";
import { decodeEvent } from "./event.ts";

export function pendingStartBrief(brief: string | undefined, turns: TranscriptTurn[]): string | undefined {
  return brief && !turns.some(turn => turn.inputs?.some(input => input.type === "session.started")) ? brief : undefined;
}

// Transcript inputs carry non-post event bodies as JSON. Decode the complete body once here.
export function timelineInputText(input: TranscriptInput, full?: string): string {
  if (input.type === "post.created") return full ?? input.text;
  if (input.type !== "session.started" || (input.more && full === undefined)) return "Started work";
  try {
    const event = decodeEvent({ id: input.id, seq: input.seq, room: input.room, at: input.at, from: input.from, type: input.type, body: JSON.parse(full ?? input.text) });
    return event?.type === "session.started" ? event.body.brief || "Started work" : "Started work";
  } catch { return "Started work"; }
}

import { stepTitle, titleOf, type StepKind } from "./steps.ts";

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

// Dyna: The transcript holds complete harness records, so a thought is finished once it appears. Only a tool call has
// a separate end: its result, which sets ended.
export function timelineItemRunning(item: TranscriptItem, turnEnded: boolean): boolean {
  return !turnEnded && item.kind === "tool" && !item.ended;
}

// Dyna: Text can fold only when a work disclosure is present.
export function visibleTimelineText(id: string, final: string | undefined, completed: boolean, hasWork: boolean, expanded: boolean): boolean {
  return !completed || !hasWork || id === final || expanded;
}

// Dyna: The transcript holds a session's own posts: nested under their tool call, or sent from a shell at the end of the items.
export function ownPost(item: TranscriptItem, session: string): boolean {
  return item.kind === "post" && item.from?.kind === "session" && item.from.session === session;
}

// Dyna: A turn's last message is its answer. It can be a post that ended the turn, so earlier text folds as working text.
// Text and a tool's posts arrive in order. Their times come from different clocks, so only a post from a shell, which
// sorts after every item, is placed by its time; equal times keep item order.
export function finalTimelineItem(items: TranscriptItem[], session: string): TranscriptItem | undefined {
  let final: TranscriptItem | undefined;
  for (const item of items) {
    for (const candidate of [item, ...(item.posts ?? [])]) {
      if (candidate.kind !== "text" && !ownPost(candidate, session)) continue;
      if (item.kind !== "post" || !final || !(Date.parse(candidate.at) < Date.parse(final.at))) final = candidate;
    }
  }
  return final;
}

// Dyna: A post names its destination only when it left the DM on screen.
export function postDestination(item: TranscriptItem, session: string, room: string, roomName: string, sessionName: (id: string) => string): string | undefined {
  if (item.home?.kind === "dm") return item.home.session && item.home.session !== session ? sessionName(item.home.session) : undefined;
  if (item.home?.kind === "feed") return item.room === room ? `#${roomName}` : "another room";
  return undefined;
}

// Dyna: One item is not a group, so it shows with no heading to open first. A script stays a group of its own actions,
// under the heading that counts them. Thoughts alone fold under one heading; with tools, they fold beside the summary.
export type WorkLayout = "single" | "thoughts" | "mixed";
export function workLayout(items: TranscriptItem[]): WorkLayout {
  if (items.length === 1 && items[0].tool !== "codemode") return "single";
  return items.every(item => item.kind === "thinking") ? "thoughts" : "mixed";
}

// Ported from T3 Code (packages/client-runtime/src/work-log/presentation.ts), MIT License, Copyright (c) 2026 T3 Tools Inc. See next/third_party/t3code/LICENSE.
// Dyna: Transcript tools have no T3 source or integration metadata.
export function summarizeWork(items: TranscriptItem[]): string {
  const tools = items.filter(item => item.kind === "tool");
  if (!tools.length) return `${items.length} ${items.length === 1 ? "thought" : "thoughts"}`;
  const groups = new Map<StepKind, number>();
  let scripts = 0;
  for (const item of tools) {
    if (item.tool === "codemode" && !item.calls?.length) { scripts++; continue; }
    const kinds = item.tool === "codemode" ? item.calls!.map(call => stepTitle({ tool: call.tool, label: call.target ?? "" }).kind) : [titleOf(item).kind];
    // Listing and fetching read as tools, so they share one label rather than repeat it.
    for (const kind of kinds) {
      const group = kind === "list" || kind === "fetch" ? "tool" : kind;
      groups.set(group, (groups.get(group) ?? 0) + 1);
    }
  }
  const selected = [...groups].map(([kind, count], index) => ({ kind, count, index }));
  const labels = selected.map(({kind, count}) => {
    const plural = count === 1 ? "" : "s";
    if (kind === "run") return `Ran ${count} command${plural}`;
    if (kind === "edit") return `Edited ${count} file${plural}`;
    if (kind === "write") return `Wrote ${count} file${plural}`;
    if (kind === "read") return `Read ${count} file${plural}`;
    if (kind === "search") return `Searched code ${count} ${count === 1 ? "time" : "times"}`;
    return `Used ${count} tool${plural}`;
  });
  if (scripts) labels.push(`Ran ${scripts} script${scripts === 1 ? "" : "s"}`);
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

// Ported from T3 Code (packages/shared/src/orchestrationTiming.ts), MIT License, Copyright (c) 2026 T3 Tools Inc. See next/third_party/t3code/LICENSE.
export function formatTimelineDuration(durationMs: number): string {
  if (!Number.isFinite(durationMs) || durationMs < 0) return "0ms";
  if (durationMs < 1_000) return `${Math.max(1, Math.round(durationMs))}ms`;
  if (durationMs < 10_000) return `${Math.round(durationMs / 100) / 10}s`;
  const totalSeconds = Math.round(durationMs / 1_000);
  if (totalSeconds < 60) return `${totalSeconds}s`;
  const hours = Math.floor(totalSeconds / 3_600);
  const minutes = Math.floor((totalSeconds % 3_600) / 60);
  const seconds = totalSeconds % 60;
  const parts: string[] = [];
  if (hours > 0) parts.push(`${hours}h`);
  if (minutes > 0) parts.push(`${minutes}m`);
  if (seconds > 0) parts.push(`${seconds}s`);
  return parts.join(" ");
}
