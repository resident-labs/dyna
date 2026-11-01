import { shortPaths } from "./feed.ts";
import type { Event, TranscriptItem, TranscriptStep } from "./contract.gen.ts";

export type StepKind = "read" | "edit" | "write" | "search" | "list" | "run" | "fetch" | "tool";

// What a step did, not which tool did it. Each harness tool name maps to one kind; any other tool keeps its own name,
// written as words, so the view never shows a tool ID (r8).
const kinds: Record<string, StepKind> = {
  read: "read", edit: "edit", write: "write", grep: "search", find: "search", web_search: "search", ls: "list", bash: "run", mcp__shell__shell: "run", web_fetch: "fetch",
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

export function titleOf(item: TranscriptItem) {
  let target = "";
  // Tool arguments are an untrusted JSON string from the harness.
  try {
    const value: unknown = JSON.parse(item.arguments ?? "{}");
    if (value && typeof value === "object" && !Array.isArray(value)) {
      const entries: [string, unknown][] = Object.entries(value);
      for (const key of ["command", "path", "query", "pattern", "url"]) {
        const field = entries.find(([name]) => name === key)?.[1];
        if (typeof field === "string") { target = field; break; }
      }
    }
  } catch { /* An incomplete preview has no row target; its fold keeps the full arguments. */ }
  const tool = item.tool ?? "tool";
  return stepTitle({ tool, label: target ? `${tool} ${target}` : "" });
}
export function activeVerb(kind: StepKind, fallback: string): string {
  const active: Record<StepKind, string> = { run: "Running", read: "Reading", edit: "Editing", write: "Writing", search: "Searching", list: "Listing", fetch: "Fetching", tool: fallback };
  return active[kind];
}
export const workingGroup = (items: TranscriptItem[], turnEnded: boolean) => !turnEnded && items.some(item => !item.ended);
export function workSummary(items: TranscriptItem[]) {
  const counts = new Map<StepKind, number>();
  for (const item of items) { const kind = titleOf(item).kind; counts.set(kind, (counts.get(kind) ?? 0) + 1); }
  const words: Record<StepKind, [string, string]> = { run: ["Ran", "command"], read: ["Read", "file"], edit: ["Edited", "file"], write: ["Wrote", "file"], search: ["Searched", "time"], list: ["Listed", "time"], fetch: ["Fetched", "page"], tool: ["Used", "tool"] };
  return [...counts].map(([kind, n], i) => { const [verb, noun] = words[kind]; return `${i ? verb.toLowerCase() : verb} ${n} ${noun}${n === 1 ? "" : "s"}`; }).join(", ");
}

// An injected clock keeps a live turn current without depending on wall time in tests.
export function elapsed(started: string, now: number): string {
  const seconds = Math.max(0, Math.floor((now - Date.parse(started)) / 1000));
  return seconds < 60 ? `${seconds} s` : `${Math.floor(seconds / 60)} m ${seconds % 60} s`;
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
