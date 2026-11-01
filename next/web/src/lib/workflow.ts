import type { WorkflowInfo, WorkflowState, WorkflowStep } from "./contract.gen.ts";
import { formatTimelineDuration } from "./timeline.ts";

type Usage = WorkflowStep["usage"];
type Tone = "failed" | "working";

// Only a phase longer than this folds by itself, so a run of many short phases reads whole.
export const LONG_PHASE = 20;

export type Phase = { key: string; title: string; steps: WorkflowStep[]; long: boolean; done: number; running: number; failed: number };

// Steps by phase, in the order each phase first ran. One phase alone needs no fold and no title of its own.
export function phases(steps: WorkflowStep[]): Phase[] {
  const groups = new Map<string, WorkflowStep[]>();
  for (const step of steps) {
    const group = groups.get(step.phase ?? "");
    if (group) group.push(step); else groups.set(step.phase ?? "", [step]);
  }
  const many = groups.size > 1;
  return [...groups].map(([key, list]) => {
    const running = list.filter(step => step.state === "running").length, failed = list.filter(step => step.state === "failed").length;
    return { key, title: key || (many ? "Other steps" : ""), steps: list, long: many && list.length > LONG_PHASE, done: list.length - running - failed, running, failed };
  });
}

// One journal row into the list. The stream and a read can cross, so an older row of the same attempt never undoes a newer one;
// a step that runs again has a later start.
export function applyStep(steps: WorkflowStep[], step: WorkflowStep): WorkflowStep[] {
  const at = steps.findIndex(item => item.key === step.key);
  if (at < 0) return [...steps, step];
  const old = steps[at];
  if (step.state === "running" && step.started === old.started && (old.state !== "running" || old.transcript)) return steps;
  return steps.with(at, step);
}

// A worker that has no file yet is waiting for its place: the box's memory or a profile's limit bounds how many run.
export function stepWord(step: WorkflowStep): { word: string; tone?: Tone } {
  if (step.state === "failed") return { word: "Failed", tone: "failed" };
  if (step.state === "done") return { word: "Done" };
  return step.kind === "agent" && !step.transcript ? { word: "Waiting" } : { word: "Running", tone: "working" };
}

export const runWords: Record<WorkflowState, { word: string; tone?: Tone }> = {
  running: { word: "Running", tone: "working" }, done: { word: "Done" }, failed: { word: "Failed", tone: "failed" }, stopped: { word: "Stopped" },
};

// A crew's line in the session list.
export const runLine = (run: Pick<WorkflowInfo, "state">, starting = false): string => `Workflow · ${starting ? "Starting" : runWords[run.state].word}`;

// A worker with a file has a transcript to open. Commands, posts and log lines have none.
export const openable = (step: WorkflowStep): boolean => step.kind === "agent" && !!step.transcript;

// The open worker is in the page's address, so Back closes it and a reload or a link keeps it.
export const shownStep = (search: string): string | undefined => new URLSearchParams(search).get("step") ?? undefined;

export const runningFor = (from: string, to: string | undefined, now: number): number => (to ? Date.parse(to) : now) - Date.parse(from);

export function stepTime(step: WorkflowStep, now: number): string {
  return step.kind === "agent" && step.state === "running" && !step.transcript ? "" : formatTimelineDuration(runningFor(step.started, step.ended, now));
}

const used = (usage: Usage) => usage.inputTokens + usage.outputTokens + usage.cacheReadTokens + usage.cacheWriteTokens;

export function total(steps: WorkflowStep[]): Usage {
  const sum: Usage = { model: "", inputTokens: 0, outputTokens: 0, cacheReadTokens: 0, cacheWriteTokens: 0 };
  for (const { usage } of steps) {
    sum.inputTokens += usage.inputTokens; sum.outputTokens += usage.outputTokens;
    sum.cacheReadTokens += usage.cacheReadTokens; sum.cacheWriteTokens += usage.cacheWriteTokens;
  }
  return sum;
}

// A step that has not ended reports no usage, so it shows nothing rather than a zero.
export function tokens(usage: Usage): string {
  const n = used(usage);
  if (!n) return "";
  if (n < 1000) return String(n);
  return n < 1_000_000 ? `${(n / 1000).toFixed(1)}k` : `${(n / 1_000_000).toFixed(1)}M`;
}

export const tokenDetail = (usage: Usage): string => used(usage)
  ? `${usage.inputTokens.toLocaleString()} in, ${usage.outputTokens.toLocaleString()} out, ${usage.cacheReadTokens.toLocaleString()} cache read, ${usage.cacheWriteTokens.toLocaleString()} cache write` : "";

// A conflict joins the journal as a failed entry so that the run counts it. The merge section shows it with its files.
export const failures = (steps: WorkflowStep[]): WorkflowStep[] => steps.filter(step => step.state === "failed" && !(step.kind === "log" && step.name.startsWith("merge conflict: ")));

// A result is what the script returned: JSON reads as code, and text as a reply.
export function resultBlock(result: string): { code: boolean; text: string } {
  try {
    const value: unknown = JSON.parse(result);
    if (typeof value === "object" && value !== null) return { code: true, text: JSON.stringify(value, null, 2) };
  } catch { /* Most text is not JSON. */ }
  return { code: false, text: result };
}

const encoded = (path: string) => path.split("/").map(encodeURIComponent).join("/");
export const branchLink = (repo: string | undefined, branch: string): string | undefined => repo ? `https://github.com/${encoded(repo)}/tree/${encoded(branch)}` : undefined;
export const commitLink = (repo: string | undefined, commit: string): string | undefined => repo ? `https://github.com/${encoded(repo)}/commit/${encodeURIComponent(commit)}` : undefined;
export const shortCommit = (commit: string): string => commit.slice(0, 7);
