import type { WorkflowInfo, WorkflowMerge, WorkflowState, WorkflowStep, WorkflowStepResponse } from "./contract.gen.ts";
import { count, formatTime } from "./feed.ts";
import { parsedJson, type Json } from "./json.ts";
import { formatTimelineDuration } from "./timeline.ts";

type Usage = WorkflowStep["usage"];
type Tone = "failed" | "working";

// Only a phase longer than this folds by itself, so a run of many short phases reads whole.
export const LONG_PHASE = 20;

// A step with the steps its workflow() ran.
export type StepNode = { step: WorkflowStep; children: StepNode[] };

export type Tally = { done: number; running: number; failed: number };
export type Phase = Tally & { key: string; title: string; nodes: StepNode[]; steps: WorkflowStep[]; long: boolean };

export function tally(steps: WorkflowStep[]): Tally {
  const running = steps.filter(step => step.state === "running").length, failed = steps.filter(step => step.state === "failed").length;
  return { done: steps.length - running - failed, running, failed };
}

// Child steps sit under the workflow step that ran them. A resumed run keeps a child until its workflow step runs again, so a
// step whose parent is not in the journal stays at the top.
export function nested(steps: WorkflowStep[]): StepNode[] {
  const nodes = new Map(steps.map(step => [step.key, { step, children: [] as StepNode[] }]));
  const top: StepNode[] = [];
  for (const node of nodes.values()) {
    const parent = node.step.parent ? nodes.get(node.step.parent) : undefined;
    (parent ?? { children: top }).children.push(node);
  }
  return top;
}

const flat = (node: StepNode): WorkflowStep[] => [node.step, ...node.children.flatMap(flat)];

// Steps by phase, in the order each phase first ran. A child belongs to its parent's phase, as a script's phase() does not
// reach into the script it calls. One phase alone needs no fold and no title of its own.
export function phases(steps: WorkflowStep[]): Phase[] {
  const groups = new Map<string, StepNode[]>();
  for (const node of nested(steps)) {
    const group = groups.get(node.step.phase ?? "");
    if (group) group.push(node); else groups.set(node.step.phase ?? "", [node]);
  }
  const many = groups.size > 1;
  return [...groups].map(([key, nodes]) => {
    const list = nodes.flatMap(flat);
    return { key, title: key || (many ? "Other steps" : ""), nodes, steps: list, long: many && list.length > LONG_PHASE, ...tally(list) };
  });
}

// The failed steps alone, each under the workflow steps that lead to it.
export function failedNodes(nodes: StepNode[]): StepNode[] {
  return nodes.flatMap(node => {
    const children = failedNodes(node.children);
    return node.step.state === "failed" || children.length ? [{ ...node, children }] : [];
  });
}

// More phases than this leave no room to tell one from the next, so the strip draws the run as one.
export const STRIP_PHASES = 12;

export type StripPart = { key: string; title: string; share: number; segments: { state: WorkflowStep["state"]; share: number }[] };

// The progress strip: each phase as wide as its share of the steps, filled with done, then failed, then running, so the live
// edge is on the right. Shares are percents of the strip and of the part. A script names its steps as it reaches them, so the
// whole is the steps known so far.
export function strip(groups: Phase[]): StripPart[] {
  const total = groups.reduce((sum, phase) => sum + phase.steps.length, 0);
  if (!total) return [];
  const parts = groups.length > STRIP_PHASES ? [{ key: "", title: "", steps: groups.flatMap(phase => phase.steps), ...tally(groups.flatMap(phase => phase.steps)) }] : groups;
  return parts.map(phase => ({
    key: phase.key, title: phase.title, share: phase.steps.length / total * 100,
    segments: (["done", "failed", "running"] as const).filter(state => phase[state]).map(state => ({ state, share: phase[state] / phase.steps.length * 100 })),
  }));
}

// Where a step ran within its run, in percents of the run's time. A live run's scale moves by the minute, so rows of finished
// steps stay still and only a running step grows.
export function runScale(info: Pick<WorkflowInfo, "started" | "ended">, now: number): { from: number; to: number } {
  const from = Date.parse(info.started);
  return { from, to: info.ended ? Date.parse(info.ended) : from + Math.max(1, Math.ceil((now - from) / 60_000)) * 60_000 };
}
// A bar under this share of the column reads as a speck, so it does not draw.
export const READABLE_SPAN = 10;
export const spanReads = (span: { width: number }): boolean => span.width >= READABLE_SPAN;
// The column shows only for a run with a bar to read. A worker that waits has run for no time yet.
export const timed = (steps: WorkflowStep[], scale: { from: number; to: number }, now: number): boolean => steps.some(step => stepTime(step, now) && spanReads(stepSpan(step, scale, now)));

export function stepSpan(step: WorkflowStep, { from, to }: { from: number; to: number }, now: number): { left: number; width: number } {
  const whole = Math.max(1, to - from), start = Math.max(0, Date.parse(step.started) - from);
  const left = Math.min(100, start / whole * 100);
  return { left, width: Math.min(100 - left, Math.max(0, (step.ended ? Date.parse(step.ended) : now) - from - start) / whole * 100) };
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

// What a run that ended came to, after its state: its feed row and its row in the sessions panel. A run from before steps were
// counted has no figure.
export const runCounts = ({ steps, failed }: { steps?: number; failed: number }): string[] => [...steps ? [count(steps, "step")] : [], ...failed ? [`${failed} failed`] : []];

// The room's runs: the running ones, newest first, then the rest, the latest to end first.
export function roomRuns(runs: Iterable<WorkflowInfo>): { running: WorkflowInfo[]; past: WorkflowInfo[] } {
  const all = [...runs], at = (run: WorkflowInfo) => Date.parse(run.ended ?? run.started);
  return { running: all.filter(run => run.state === "running").sort((a, b) => Date.parse(b.started) - Date.parse(a.started)),
    past: all.filter(run => run.state !== "running").sort((a, b) => at(b) - at(a)) };
}

// The runs' rows in the sessions panel, under a "Workflows" heading: the first few running runs, a row that counts the rest and,
// open, lists them, then the same for the past runs, so that many runs never bury the sessions. The past row counts the failures
// of today, which are news. keep is the open page's run, which stays listed under a closed fold. A room with no runs has
// no rows.
export const RUNNING_SHOWN = 4;
export type RunRow = { kind: "head" } | { kind: "run"; run: WorkflowInfo } | { kind: "more"; count: number; open: boolean } | { kind: "past"; count: number; failed: number; open: boolean };
export function runRows(runs: Iterable<WorkflowInfo>, open: { running: boolean; past: boolean }, keep?: string): RunRow[] {
  const { running, past } = roomRuns(runs), row = (run: WorkflowInfo): RunRow => ({ kind: "run", run });
  // The runs under a fold: all of them when it is open, else only the open page's.
  const folded = (list: WorkflowInfo[], opened: boolean) => list.filter(run => opened || run.session === keep).map(row);
  const today = new Date().toDateString();
  const rows: RunRow[] = [...running.slice(0, RUNNING_SHOWN).map(row),
    ...running.length > RUNNING_SHOWN ? [{ kind: "more" as const, count: running.length - RUNNING_SHOWN, open: open.running }, ...folded(running.slice(RUNNING_SHOWN), open.running)] : [],
    ...past.length ? [{ kind: "past" as const, count: past.length, failed: past.filter(run => run.state === "failed" && new Date(run.ended ?? run.started).toDateString() === today).length, open: open.past }, ...folded(past, open.past)] : []];
  return rows.length ? [{ kind: "head" }, ...rows] : [];
}

// The line a script that never called status() gets, as "75 of 76 steps · 1 running".
export function countedLine({ steps, running, failed }: { steps: number; running: number; failed: number }): string {
  if (!steps) return "No steps yet";
  return [running ? `${steps - running} of ${steps} steps` : count(steps, "step"), ...running ? [`${running} running`] : [], ...failed ? [`${failed} failed`] : []].join(" · ");
}

// A worker with a file has a transcript to open.
export const openable = (step: WorkflowStep): boolean => step.kind === "agent" && !!step.transcript;
// A step that holds more than its row opens in place. One with nothing more has nothing to open, and a workflow step folds its children.
export const foldable = (step: WorkflowStep): boolean => !openable(step) && step.kind !== "workflow" && !!step.more;

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

const encoded = (path: string) => path.split("/").map(encodeURIComponent).join("/");
export const branchLink = (repo: string | undefined, branch: string): string | undefined => repo ? `https://github.com/${encoded(repo)}/tree/${encoded(branch)}` : undefined;
export const commitLink = (repo: string | undefined, commit: string): string | undefined => repo ? `https://github.com/${encoded(repo)}/commit/${encodeURIComponent(commit)}` : undefined;
export const shortCommit = (commit: string): string => commit.slice(0, 7);

// JSON reads for a person here, on any script's shape: no raw JSON shows on the run page. Each value is a labelled row, a short
// list is text, an object is a group, and what is too deep or too long says how much there is.
export type Field = { label: string } & ({ kind: "text"; text: string } | { kind: "commit"; commit: string } | { kind: "branch"; branch: string } | { kind: "link"; href: string }
  | { kind: "list"; items: Field[]; inline: boolean } | { kind: "group"; fields: Field[] });

// What the page already says, so a result does not say it twice: facts anywhere, names only inside a list, and counts by key.
export type Shown = { facts: Set<string>; names: Set<string>; counts: Record<string, number> };
const nothingShown: Shown = { facts: new Set(), names: new Set(), counts: {} };

const DEPTH = 3, LIST = 20, OBJECTS = 10, TEXT = 4000, INLINE = 64, TITLE = 60;

export function shownFacts(info: Pick<WorkflowInfo, "branch" | "name" | "repo" | "commit">, steps: WorkflowStep[], merge?: WorkflowMerge | null): Shown {
  const conflicts = merge?.conflicts ?? [];
  const commits = [info.commit, merge?.commit].flatMap(commit => commit ? [commit, shortCommit(commit)] : []);
  const facts = new Set([...commits, info.repo ?? "", merge?.branch ?? "", ...conflicts.map(conflict => conflict.branch ?? "")].filter(Boolean));
  // A worker in its own tree names its branch for its step, so a script's list of what those steps left names them too.
  const names = new Set([info.name, ...steps.flatMap(step => [step.key, step.name, `${info.branch}-${step.key.slice(0, 12)}`]), ...merge?.merged ?? [], ...conflicts.map(conflict => conflict.step)]);
  return { facts, names, counts: { steps: steps.length, failed: tally(steps).failed, ...merge && { merged: merge.merged?.length ?? 0, conflicts: conflicts.length } } };
}

const record = (value: Json): value is { [key: string]: Json } => typeof value === "object" && value !== null && !Array.isArray(value);
const empty = (value: Json) => value === null || value === "" || (Array.isArray(value) ? !value.length : record(value) && !Object.keys(value).length);

// A step that said where it landed names a merge or a step the page shows, whatever else it carries.
const identity = ["step", "key", "name", "label", "branch", "commit"];
function repeats(value: Json, shown: Shown): boolean {
  const known = (item: Json) => typeof item === "string" && (shown.facts.has(item) || shown.names.has(item));
  if (typeof value === "string") return shown.facts.has(value);
  if (Array.isArray(value)) return !!value.length && value.every(item => known(item) || record(item) && identity.some(key => known(item[key] ?? null)));
  return record(value) && [value.branch, value.commit].some(item => typeof item === "string" && shown.facts.has(item));
}

const shortNames = /\b(id|url|pr|ci|sha|api|sql|ui|http|html|json)\b/g;
const milliseconds = /(?:_ms|Ms)$/;
// "filesChanged" and "files_changed" both read "Files changed".
function humanized(key: string): string {
  const words = key.replace(milliseconds, "").replace(/([a-z0-9])([A-Z])/g, "$1 $2").replace(/[_-]+/g, " ").trim().toLowerCase().replace(shortNames, name => name.toUpperCase());
  return words.charAt(0).toUpperCase() + words.slice(1);
}

function stringField(label: string, key: string, text: string, depth: number, shown: Shown): Field | undefined {
  const inner = parsedJson(text);
  if (inner !== undefined) return field(label, key, inner, depth, shown);
  if (/^https?:\/\/\S+$/.test(text)) return { label, kind: "link", href: text };
  if (/^[0-9a-f]{40}$/.test(text) || /^[0-9a-f]{7,39}$/.test(text) && /commit|sha/i.test(key)) return { label, kind: "commit", commit: text };
  if (/branch/i.test(key) && /^\S+$/.test(text)) return { label, kind: "branch", branch: text };
  if (/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}/.test(text) && !Number.isNaN(Date.parse(text))) return { label, kind: "text", text: formatTime(text) };
  return { label, kind: "text", text: text.length > TEXT ? `${text.length.toLocaleString()} characters` : text };
}

function listField(label: string, key: string, items: Json[], depth: number, shown: Shown): Field | undefined {
  const kept = items.filter(item => !empty(item));
  if (!kept.length) return undefined;
  const plain = kept.every(item => typeof item !== "object");
  if (kept.length > (plain ? LIST : OBJECTS) || !plain && depth >= DEPTH) return { label, kind: "text", text: count(kept.length, "item") };
  if (plain) {
    const list = kept.flatMap(item => field("", key, item, depth, shown) ?? []);
    return { label, kind: "list", items: list, inline: list.every(item => item.kind !== "link") && kept.join(", ").length <= INLINE };
  }
  // An object in a list goes by its first short text, which is what most lists of things start with.
  const groups = kept.flatMap((item, at): Field[] => {
    if (!record(item)) return [field(`${at + 1}`, key, item, depth + 1, shown)].filter(entry => !!entry);
    const title = Object.entries(item).find(([, value]) => typeof value === "string" && value.length <= TITLE);
    const rest = title ? Object.fromEntries(Object.entries(item).filter(([name]) => name !== title[0])) : item;
    const fields = objectFields(rest, depth + 1, shown);
    const name = title ? String(title[1]) : `${at + 1}`;
    return fields.length ? [{ label: name, kind: "group", fields }] : [{ label: "", kind: "text", text: name }];
  });
  return groups.length ? { label, kind: "group", fields: groups } : undefined;
}

function field(label: string, key: string, value: Json, depth: number, shown: Shown): Field | undefined {
  if (value === null || empty(value)) return undefined;
  if (typeof value === "boolean") return { label, kind: "text", text: value ? "Yes" : "No" };
  if (typeof value === "number") return { label, kind: "text", text: milliseconds.test(key) ? formatTimelineDuration(value) : value.toLocaleString() };
  if (typeof value === "string") return stringField(label, key, value, depth, shown);
  if (Array.isArray(value)) return listField(label, key, value, depth, shown);
  if (depth >= DEPTH) return { label, kind: "text", text: count(Object.keys(value).length, "field") };
  const fields = objectFields(value, depth + 1, shown);
  return fields.length ? { label, kind: "group", fields } : undefined;
}

function objectFields(value: { [key: string]: Json }, depth: number, shown: Shown): Field[] {
  return Object.entries(value).flatMap(([key, item]) => repeats(item, shown) || shown.counts[key] === item ? [] : field(humanized(key), key, item, depth, shown) ?? []);
}

export function jsonFields(value: Json, shown: Shown = nothingShown): Field[] {
  if (Array.isArray(value)) return repeats(value, shown) ? [] : [listField("Items", "items", value, 0, shown)].filter(entry => !!entry);
  return record(value) ? objectFields(value, 0, shown) : [];
}

// What a script returned or a command wrote: JSON reads as fields, and anything else as the text it is.
export type Reading = { text: string; fields?: undefined } | { fields: Field[]; text?: undefined };
export function reading(text: string, shown?: Shown): Reading {
  const value = parsedJson(text);
  return value === undefined ? { text } : { fields: jsonFields(value, shown) };
}

export const mergeFields = (merge: WorkflowMerge): Field[] => [
  { label: "Branch", kind: "branch", branch: merge.branch }, { label: "Commit", kind: "commit", commit: merge.commit },
  ...merge.merged?.length ? [{ label: "Merged", kind: "text" as const, text: merge.merged.join(", ") }] : [],
];

// One part of an opened step, each under an optional label.
export type StepPart = { label?: string } & ({ kind: "code" | "text" | "reason" | "markdown"; text: string } | { kind: "fields"; fields: Field[] });

const partOf = (label: string | undefined, text: string): StepPart[] => {
  const read = reading(text);
  return read.fields ? (read.fields.length ? [{ label, kind: "fields", fields: read.fields }] : []) : [{ label, kind: "code", text }];
};

// run() returns this shape, and merge() and post() their own.
function commandParts(step: WorkflowStep, result: { [key: string]: Json }): StepPart[] {
  const text = (value: Json | undefined) => typeof value === "string" ? value.trim() : "";
  const out = text(result.stdout), err = text(result.stderr), code = typeof result.code === "number" ? result.code : 0;
  return [
    ...out ? partOf("Output", out) : [],
    ...err && !step.reason?.includes(err) ? partOf("Errors", err) : [],
    ...code && !step.reason ? [{ label: "Exit code", kind: "text" as const, text: String(code) }] : [],
  ];
}

const postData = /\s*```json\n([\s\S]*)\n```\s*$/;

// What a step shows when it opens: why it failed, what it ran, and what that left. A post's receipt and a log's null say
// nothing, so only a command's result shows, and a post's data reads as fields, not as its JSON fence.
export function stepBody(step: WorkflowStep, output?: WorkflowStepResponse): StepPart[] {
  const detail = output?.detail?.trim() ?? "";
  const parts: StepPart[] = step.reason ? [{ kind: "reason", text: step.reason }] : [];
  if (step.kind === "post") {
    const data = postData.exec(detail)?.[1], text = detail.replace(postData, "").trim();
    if (text) parts.push({ kind: "markdown", text });
    const read = data ? reading(data) : undefined;
    if (read?.fields?.length) parts.push({ kind: "fields", fields: read.fields });
  } else if (step.kind === "run") {
    if (detail && step.name !== `run: ${detail}`) parts.push({ kind: "code", label: "Command", text: detail });
    const value = output?.result ? parsedJson(output.result) : undefined;
    if (value !== undefined && record(value) && ("stdout" in value || "stderr" in value)) parts.push(...commandParts(step, value));
    else if (value !== undefined) { const fields = jsonFields(value); if (fields.length) parts.push({ kind: "fields", fields }); }
  } else if (detail && detail !== step.reason) {
    const read = reading(detail);
    if (read.fields) { if (read.fields.length) parts.push({ kind: "fields", fields: read.fields }); }
    else parts.push({ kind: "text", text: detail, ...step.kind === "agent" && { label: "Prompt" } });
  }
  return parts;
}

// A run as its flowchart and timeline draw it. Nothing is read from a step's name: its phase is its stage and its lane the
// subject it is about, as the script tagged them.

// What a view calls a step: its lane, or its label.
export const stepTitle = (step: WorkflowStep): string => step.lane || step.name;

const byStart = (steps: WorkflowStep[]): WorkflowStep[] => steps.map((step, at) => ({ step, at, time: Date.parse(step.started) }))
  .sort((a, b) => a.time - b.time || a.at - b.at).map(item => item.step);

// Each step's stage: its phase, or for a child of a workflow() step that set none, its parent's.
export function stageOf(steps: WorkflowStep[]): Map<string, string> {
  const byKey = new Map(steps.map(step => [step.key, step]));
  const stages = new Map<string, string>();
  const of = (step: WorkflowStep, depth: number): string => {
    const known = stages.get(step.key);
    if (known !== undefined) return known;
    const parent = !step.phase && step.parent && depth < 8 ? byKey.get(step.parent) : undefined;
    const stage = step.phase || (parent ? of(parent, depth + 1) : "");
    stages.set(step.key, stage);
    return stage;
  };
  for (const step of steps) of(step, 0);
  return stages;
}

export const STAGE_COLOURS = 6;
export type Stage = { key: string; title: string; colour: string };

// A run's stages in the order each first ran. Each named stage takes the next stage colour, and steps with none read muted.
export function stagesOf(steps: WorkflowStep[], stages = stageOf(steps)): Stage[] {
  const keys = [...new Set(byStart(steps).map(step => stages.get(step.key) ?? ""))];
  let named = 0;
  return keys.map(key => ({ key, title: key || (keys.length > 1 ? "Other steps" : "Steps"), colour: key ? `var(--stage-${named++ % STAGE_COLOURS + 1})` : "var(--muted)" }));
}

export type FlowNode = Stage & Tally & { steps: WorkflowStep[]; lanes: number; live: WorkflowStep[] };
// A count of 0 is an order, not an observed path: a run without lanes, or a stage that no lane reached.
export type FlowEdge = { from: string; to: string; count: number };
// The failed node's key. A phase is text a script gave, and a script cannot give this.
export const FAILED = "\u0000failed";

// The flowchart: a node per stage with its counts, and an edge for each move a lane made from one stage to the next, counted,
// so a loop and a skip show as the run made them. A failed node joins only when a step failed.
export function flow(steps: WorkflowStep[]): { nodes: FlowNode[]; edges: FlowEdge[]; failed: WorkflowStep[]; laned: boolean } {
  const ordered = byStart(steps.filter(step => step.kind !== "workflow"));
  const stages = stageOf(steps);
  const stage = (step: WorkflowStep) => stages.get(step.key) ?? "";
  const nodes = stagesOf(ordered, stages).map(item => {
    const own = ordered.filter(step => stage(step) === item.key);
    return { ...item, steps: own, lanes: new Set(own.flatMap(step => step.lane ? [step.lane] : [])).size, live: own.filter(step => step.state === "running"), ...tally(own) };
  });
  const paths = new Map<string, string[]>();
  for (const step of ordered) {
    if (!step.lane) continue;
    const path = paths.get(step.lane) ?? [];
    if (path.at(-1) !== stage(step)) path.push(stage(step));
    paths.set(step.lane, path);
  }
  const edges: FlowEdge[] = [];
  const add = (from: string, to: string, count: number) => {
    const edge = edges.find(item => item.from === from && item.to === to);
    if (edge) edge.count += count; else edges.push({ from, to, count });
  };
  for (const path of paths.values()) for (let at = 1; at < path.length; at++) add(path[at - 1], path[at], 1);
  // A stage no lane reached joins the stages before and after it, in the order they first ran, as every stage of a run
  // without lanes does.
  const reached = new Set(edges.flatMap(edge => [edge.from, edge.to]));
  nodes.forEach((node, at) => {
    if (reached.has(node.key)) return;
    if (at > 0) add(nodes[at - 1].key, node.key, 0);
    if (at < nodes.length - 1) add(node.key, nodes[at + 1].key, 0);
  });
  for (const node of nodes) if (node.failed) add(node.key, FAILED, node.failed);
  return { nodes, edges, failed: ordered.filter(step => step.state === "failed"), laned: paths.size > 0 };
}

// "<1m", "28m", "1h" or "2h 5m" from a span in milliseconds.
export function minutesLabel(ms: number): string {
  if (ms < 60_000) return "<1m";
  const minutes = Math.round(ms / 60_000), hours = Math.floor(minutes / 60);
  return minutes < 60 ? `${minutes}m` : minutes % 60 ? `${hours}h ${minutes % 60}m` : `${hours}h`;
}

export type Bar = { step: WorkflowStep; row: number; left: number; width: number; kept: boolean; colour: string };
export type Chart = { rows: number; bars: Bar[]; commands: Bar[]; fold: number; resumed: string; now?: number; ticks: { at: number; label: string }[] };
// The share of the chart that the steps a resumed run kept fold into.
export const FOLD = 16;
const TICK_MINUTES = [1, 2, 5, 10, 15, 20, 30, 60, 120, 180, 360, 720, 1440];

// The timeline: worker slots as rows, packed from each step's start and end, so a step takes the first row free when it
// starts. Workers are the agent steps; a run without any packs whatever ran. Commands and posts draw on a row of their own.
// Steps a resumed run kept fold into a band on the left, packed apart because another box ran them. A worker that waits
// for its place has run for no time yet, so it has no bar.
export function timeline(info: Pick<WorkflowInfo, "started" | "ended" | "state">, steps: WorkflowStep[], now: number): Chart {
  const stages = stageOf(steps), colours = new Map(stagesOf(steps, stages).map(item => [item.key, item.colour]));
  const resume = Date.parse(info.started);
  const start = (step: WorkflowStep) => Date.parse(step.started);
  const end = (step: WorkflowStep) => Math.max(start(step), step.ended ? Date.parse(step.ended) : now);
  const shown = byStart(steps).filter(step => step.kind !== "workflow" && step.kind !== "log" && stepTime(step, now));
  const kept = shown.filter(step => start(step) < resume);
  const to = Math.max(info.ended ? Date.parse(info.ended) : now, ...shown.filter(step => start(step) >= resume).map(end), resume + 60_000);
  const keptFrom = kept.length ? Math.min(...kept.map(start)) : resume, keptTo = kept.length ? Math.max(...kept.map(end)) : resume;
  const fold = kept.length ? FOLD : 0;
  const x = (time: number) => time < resume ? (keptTo > keptFrom ? fold * Math.min(1, (time - keptFrom) / (keptTo - keptFrom)) : 0)
    : fold + (100 - fold) * Math.min(1, (time - resume) / (to - resume));
  const bar = (step: WorkflowStep, row: number): Bar => ({ step, row, left: x(start(step)), width: Math.max(0, x(end(step)) - x(start(step))), kept: start(step) < resume,
    colour: colours.get(stages.get(step.key) ?? "") ?? "var(--muted)" });
  const pack = (list: WorkflowStep[]): Bar[] => {
    const free: number[] = [];
    return list.map(step => {
      let row = free.findIndex(at => at <= start(step));
      if (row < 0) row = free.push(0) - 1;
      free[row] = end(step);
      return bar(step, row);
    });
  };
  const agents = shown.some(step => step.kind === "agent");
  const workers = shown.filter(step => !agents || step.kind === "agent");
  const bars = [...pack(workers.filter(step => start(step) < resume)), ...pack(workers.filter(step => start(step) >= resume))];
  const commands = agents ? shown.filter(step => step.kind !== "agent").map(step => bar(step, 0)) : [];
  // Ticks count from the first step, at a round interval that leaves at most six over the run's own time.
  const span = (to - resume) / 60_000, every = (TICK_MINUTES.find(minutes => span / minutes <= 6) ?? 1440) * 60_000;
  const ticks = [{ at: 0, label: "0m" }];
  for (let offset = Math.ceil((resume - keptFrom) / every) * every || every; keptFrom + offset <= to; offset += every) {
    const at = x(keptFrom + offset);
    if (at - ticks[ticks.length - 1].at >= 6 && at <= 94) ticks.push({ at, label: minutesLabel(offset) });
  }
  const earlier = steps.filter(step => start(step) < resume).length;
  return { rows: Math.max(1, ...bars.map(item => item.row + 1)), bars, commands, fold, ticks, now: info.state === "running" ? x(now) : undefined,
    resumed: kept.length ? `Resumed at ${minutesLabel(resume - keptFrom)}, ${count(earlier, "earlier step")} kept` : "" };
}

export type Figures = { work: number; split: (Stage & { ms: number })[]; longest?: { step: WorkflowStep; ms: number }; wall: number; earlier: number; usage: Usage };

// The figures under the timeline: worker time and its split by stage, the longest step, the wall time with what ran before a
// resume, and the tokens, which a kept step does not carry.
export function figures(info: Pick<WorkflowInfo, "started" | "ended">, steps: WorkflowStep[], now: number): Figures {
  const shown = steps.filter(step => step.kind !== "workflow" && step.kind !== "log" && stepTime(step, now));
  const workers = shown.some(step => step.kind === "agent") ? shown.filter(step => step.kind === "agent") : shown;
  const took = (step: WorkflowStep) => runningFor(step.started, step.ended, now);
  const stages = stageOf(steps);
  const split = stagesOf(steps, stages).map(item => ({ ...item, ms: workers.filter(step => (stages.get(step.key) ?? "") === item.key).reduce((sum, step) => sum + took(step), 0) }))
    .filter(part => part.ms > 0).sort((a, b) => b.ms - a.ms);
  const longest = workers.reduce<{ step: WorkflowStep; ms: number } | undefined>((best, step) => !best || took(step) > best.ms ? { step, ms: took(step) } : best, undefined);
  const resume = Date.parse(info.started), kept = steps.filter(step => Date.parse(step.started) < resume);
  const earlier = kept.length ? Math.max(...kept.map(step => Date.parse(step.ended ?? step.started))) - Math.min(...kept.map(step => Date.parse(step.started))) : 0;
  return { work: split.reduce((sum, part) => sum + part.ms, 0), split, longest, wall: runningFor(info.started, info.ended, now), earlier, usage: total(steps) };
}
