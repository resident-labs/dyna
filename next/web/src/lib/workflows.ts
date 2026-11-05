import type { BoxSizeName, ProfileName, WorkflowEffective, WorkflowFeature, WorkflowScope, WorkflowSetRequest, WorkflowSetting, WorkflowSettings, WorkflowStarters } from "./contract.gen.ts";
import type { Chip } from "./settings-list.ts";

// Settings > Workflows (spec 5.13, 9.2): the workflow settings of the global default and of each room, from
// workflow.settings, which also gives each setting's one line of help in the order the page shows them; the page uses
// that line and adds only a short name. A value a scope does not set is inherited: a room's from the global default, the
// global default's from Dyna's built-in default. Each change is one workflow.set naming one setting.

export const globalKey = "global";
// A room's ID, or "global", which no room ID is.
export const scopeKey = (scope: WorkflowScope) => scope.room ?? globalKey;
export const scopeName = (scope: WorkflowScope) => scope.room ? scope.name || scope.room : "Global default";

// The global default, then the rooms by name with digits as numbers.
export const scopeOrder = (global: WorkflowScope, rooms: readonly WorkflowScope[]): WorkflowScope[] =>
  [global, ...[...rooms].sort((a, b) => scopeName(a).localeCompare(scopeName(b), "en", { numeric: true, sensitivity: "base" }))];

export type SettingKind = "start" | "count" | "timeout" | "size" | "profiles" | "switch";
// Each setting's short name and control. The Record names every setting, so a new one fails the type check here.
export const settingShape: Record<WorkflowSetting, { label: string; kind: SettingKind }> = {
  start: { label: "Who may start", kind: "start" },
  maxRuns: { label: "Runs at once", kind: "count" },
  maxWorkers: { label: "Workers per run", kind: "count" },
  profiles: { label: "Profiles", kind: "profiles" },
  size: { label: "Default size", kind: "size" },
  timeout: { label: "Worker timeout", kind: "timeout" },
  worktree: { label: "Worktrees", kind: "switch" },
  merge: { label: "Merge", kind: "switch" },
  run: { label: "Shell commands", kind: "switch" },
  workflow: { label: "Nested workflows", kind: "switch" },
  post: { label: "Posts to the parent", kind: "switch" },
  resume: { label: "Resume", kind: "switch" },
};
export const starters: Record<WorkflowStarters, string> = { any: "any session", leads: "leads", root: "root only", off: "off" };
export const starterOrder: readonly WorkflowStarters[] = ["any", "leads", "root", "off"];
export const boxSizes: readonly BoxSizeName[] = ["agent", "small", "medium", "large", "xlarge"];
// The caps workflow.set holds them to (contract.MaxWorkflowCount, contract.MaxWorkflowTimeout), for the fields' hints.
export const maxCount = 10000, maxTimeout = 30 * 24 * 60 * 60;

// Whether a scope sets a value: off and 0 are values, and an empty list, as the engine reads it, is none.
function isSet(set: WorkflowSettings, setting: WorkflowSetting): boolean {
  const value = set[setting];
  return value !== undefined && value !== null && !(Array.isArray(value) && value.length === 0);
}
// The settings a scope sets, in the engine's order.
export const overrides = (scope: WorkflowScope, order: readonly WorkflowSetting[]) => order.filter(setting => isSet(scope.set, setting));
export const source = (scope: WorkflowScope, setting: WorkflowSetting): "here" | "global" | "default" =>
  isSet(scope.set, setting) ? "here" : scope.room ? "global" : "default";

const unit = (n: number, one: string) => `${n.toLocaleString("en")} ${one}${n === 1 ? "" : "s"}`;
// Seconds in the largest whole unit, up to days.
export function duration(seconds: number): string {
  if (seconds > 0 && seconds % 86400 === 0) return unit(seconds / 86400, "day");
  if (seconds > 0 && seconds % 3600 === 0) return unit(seconds / 3600, "hour");
  if (seconds > 0 && seconds % 60 === 0) return unit(seconds / 60, "minute");
  return unit(seconds, "second");
}

// A value in force as a row names it.
export function valueText(effective: WorkflowEffective, setting: WorkflowSetting): string {
  switch (setting) {
    case "start": return starters[effective.start];
    case "maxRuns": case "maxWorkers": return effective[setting] ? effective[setting].toLocaleString("en") : "no limit";
    case "profiles": return effective.profiles?.length ? effective.profiles.join(", ") : "all profiles";
    case "size": return effective.size || "the engine's choice";
    case "timeout": return duration(effective.timeout);
    default: return effective[setting] ? "on" : "off";
  }
}

// A row's line: the values its scope sets, or what it follows.
export function scopeLine(scope: WorkflowScope, order: readonly WorkflowSetting[]): string {
  const own = overrides(scope, order);
  if (!own.length) return scope.room ? "Uses global" : "Built-in defaults";
  return own.map(setting => `${settingShape[setting].label} ${valueText(scope.effective, setting)}`).join(" · ");
}

export const workflowChips = (order: readonly WorkflowSetting[]): Chip<WorkflowScope>[] => [
  { id: "all", label: "All" },
  { id: "overrides", label: "Overrides", test: scope => !!scope.room && overrides(scope, order).length > 0 },
  { id: "inherits", label: "Uses global", test: scope => !!scope.room && overrides(scope, order).length === 0 },
];

export function workflowSummary(scopes: readonly WorkflowScope[], order: readonly WorkflowSetting[]) {
  const rooms = scopes.filter(scope => scope.room);
  const global = scopes.find(scope => !scope.room);
  return { rooms: rooms.length, overriding: rooms.filter(scope => overrides(scope, order).length).length, global: global ? overrides(global, order).length : 0 };
}

export type WorkflowChange =
  | { setting: "start"; value: WorkflowStarters }
  | { setting: "maxRuns" | "maxWorkers" | "timeout"; value: number }
  | { setting: "profiles"; value: ProfileName[] }
  | { setting: "size"; value: BoxSizeName }
  | { setting: WorkflowFeature; value: boolean };
const scoped = (scope: WorkflowScope, request: Omit<WorkflowSetRequest, "room">): WorkflowSetRequest => scope.room ? { room: scope.room, ...request } : request;
// The workflow.set of one change: the room's, or with no room the global default's.
export function setRequest(scope: WorkflowScope, change: WorkflowChange): WorkflowSetRequest {
  const set: WorkflowSettings = {};
  switch (change.setting) {
    case "start": set.start = change.value; break;
    case "maxRuns": case "maxWorkers": case "timeout": set[change.setting] = change.value; break;
    case "profiles": set.profiles = change.value; break;
    case "size": set.size = change.value; break;
    default: set[change.setting] = change.value;
  }
  return scoped(scope, { set });
}
// Use global, or Use default on the global default: the setting inherits again.
export const clearRequest = (scope: WorkflowScope, setting: WorkflowSetting): WorkflowSetRequest => scoped(scope, { set: {}, clear: [setting] });

// The workflow.set for a number field's text, or undefined when it holds no whole number or the value in force, so the
// field is put back. The engine holds the caps and refuses past them in its words.
export function numberChange(scope: WorkflowScope, setting: "maxRuns" | "maxWorkers" | "timeout", text: string): WorkflowSetRequest | undefined {
  const n = text.trim() === "" ? NaN : Number(text);
  if (!Number.isSafeInteger(n) || n === scope.effective[setting]) return undefined;
  return setRequest(scope, { setting, value: n });
}

// The profiles in force with one ticked or unticked, in profile.list's order, a set profile it no longer lists after
// them; every listed profile is in force when none is set. Unticking the last returns a set value to inheriting, as an
// empty list is no value; undefined when nothing changes, so the box is put back.
export function profilesChange(scope: WorkflowScope, listed: readonly ProfileName[], name: ProfileName, on: boolean): WorkflowSetRequest | undefined {
  const now = scope.effective.profiles?.length ? scope.effective.profiles : listed;
  if (now.includes(name) === on) return undefined;
  const wanted = new Set(on ? [...now, name] : now.filter(each => each !== name));
  const next = [...listed.filter(each => wanted.has(each)), ...[...wanted].filter(each => !listed.includes(each))];
  if (next.length) return setRequest(scope, { setting: "profiles", value: next });
  return isSet(scope.set, "profiles") ? clearRequest(scope, "profiles") : undefined;
}
// The profile boxes: profile.list's and any profile in force it no longer has, ticked for those in force, every one when none is set.
export function profileChoices(listed: readonly ProfileName[], effective: WorkflowEffective): { name: ProfileName; on: boolean }[] {
  const now = effective.profiles ?? [];
  return [...listed, ...now.filter(each => !listed.includes(each))].map(name => ({ name, on: !now.length || now.includes(name) }));
}

// Runs tasks one at a time in the order they come, each after the last has settled, whether it worked or not; pending
// counts those not settled yet, so a read that lands meanwhile can be dropped.
export function serial() {
  let last: Promise<unknown> = Promise.resolve(), waiting = 0;
  const run = <T>(task: () => Promise<T>): Promise<T> => {
    waiting++;
    const next = last.then(task, task);
    last = next.then(() => { waiting--; }, () => { waiting--; });
    return next;
  };
  return Object.assign(run, { pending: () => waiting });
}
