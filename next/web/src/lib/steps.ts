import { shortPaths } from "./feed.ts";
import type { Event, TranscriptStep } from "./contract.gen.ts";

export type StepKind = "read" | "edit" | "write" | "search" | "list" | "run" | "fetch" | "tool";

// What a step did, not which tool did it. Each harness tool name maps to one kind; any other tool keeps its own name,
// written as words, so the view never shows a tool ID (r8).
const kinds: Record<string, StepKind> = {
  read: "read", edit: "edit", write: "write", grep: "search", find: "search", web_search: "search", ls: "list", bash: "run", web_fetch: "fetch",
};
const verbs: Record<Exclude<StepKind, "tool">, string> = {
  read: "Read", edit: "Edited", write: "Wrote", search: "Searched", list: "Listed", run: "Ran", fetch: "Fetched",
};

// The transcript's label is the tool name and then its target, as "bash go test ./...". target is short for the row;
// full is for its tooltip.
export function stepTitle({ tool, label }: Pick<TranscriptStep, "tool" | "label">): { kind: StepKind; verb: string; target: string; full: string } {
  const kind = kinds[tool] ?? "tool";
  const words = tool.split("__").at(-1)!.replace(/[_-]+/g, " ").trim();
  const verb = kind === "tool" ? words.charAt(0).toUpperCase() + words.slice(1) : verbs[kind];
  const full = label.startsWith(tool) ? label.slice(tool.length).trim() : label;
  return { kind, verb, target: shortPaths(full), full };
}

// A step's own time, as in v1: a quick step reads in milliseconds, so it does not look like a clock that failed to move.
// A step with no end has no time.
export function stepTime({ started, ended }: Pick<TranscriptStep, "started" | "ended">): string {
  const ms = ended ? Date.parse(ended) - Date.parse(started) : 0;
  if (ms <= 0) return "";
  if (ms < 1000) return `${ms} ms`;
  return ms < 60_000 ? `${(ms / 1000).toFixed(1)} s` : `${Math.floor(ms / 60_000)} min ${Math.floor(ms % 60_000 / 1000)} s`;
}

// The turns a work row covers. The count of marks changes when a turn ends, so a row read mid-turn reads again.
export function rowTurns(events: Event[]): { turns: string[]; key: string } {
  const marks = events.flatMap(event => event.type === "turn.started" || event.type === "turn.ended" ? [event.body.turn] : []);
  const turns = [...new Set(marks)];
  return { turns, key: `${turns.join()}:${marks.length}` };
}
