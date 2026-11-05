import { test } from "node:test";
import assert from "node:assert/strict";
import { refusedSetting, clearRequest, duration, numberChange, overrides, profileChoices, profilesChange, scopeKey, scopeLine, scopeName, scopeOrder, serial, setRequest, source,
  valueText, workflowChips, workflowSummary } from "./workflows.ts";
import type { WorkflowEffective, WorkflowScope, WorkflowSetting, WorkflowSettings } from "./contract.gen.ts";

// The engine's built-in defaults (contract.WorkflowDefaults) and its order of the settings (contract.AllWorkflowSettings).
const defaults: WorkflowEffective = { start: "any", maxRuns: 0, maxWorkers: 0, timeout: 18000, worktree: true, merge: true, run: true, workflow: true, post: true, resume: true };
const order: WorkflowSetting[] = ["start", "maxRuns", "maxWorkers", "profiles", "size", "timeout", "worktree", "merge", "run", "workflow", "post", "resume"];
const global = (set: WorkflowSettings = {}, effective: Partial<WorkflowEffective> = {}): WorkflowScope => ({ set, effective: { ...defaults, ...effective } });
const room = (id: string, name: string, set: WorkflowSettings = {}, effective: Partial<WorkflowEffective> = {}): WorkflowScope => ({ room: id, name, set, effective: { ...defaults, ...effective } });

test("the global default comes first, then the rooms by name with digits as numbers, and the engine's list is not changed", () => {
  const rooms = [room("r3", "web-10"), room("r1", "dyna"), room("r2", "web-9"), room("r4", "Atlas")];
  const scopes = scopeOrder(global(), rooms);
  assert.deepEqual(scopes.map(scopeName), ["Global default", "Atlas", "dyna", "web-9", "web-10"]);
  assert.deepEqual(scopes.map(scopeKey), ["global", "r4", "r1", "r2", "r3"]);
  assert.equal(rooms[0].room, "r3");
  // A room the engine sends without a name shows its ID rather than nothing.
  assert.equal(scopeName(room("r9", "")), "r9");
});

test("a scope overrides the values it sets, in the engine's order; off and 0 are values, an empty list or text is not", () => {
  assert.deepEqual(overrides(room("r", "a", { worktree: false, maxRuns: 0, start: "root" }), order), ["start", "maxRuns", "worktree"]);
  assert.deepEqual(overrides(room("r", "a", { start: undefined, maxRuns: null, profiles: [], size: undefined, timeout: null, post: null }), order), []);
  assert.deepEqual(overrides(global({ profiles: ["fast"], size: "large", timeout: 60 }), order), ["profiles", "size", "timeout"]);
  // The engine's order wins over the order the values arrive in.
  assert.deepEqual(overrides(room("r", "a", { resume: true, start: "off" }), ["resume", "start"]), ["resume", "start"]);
});

test("a value comes from the scope that sets it, else from the global default for a room, else from the built-in default", () => {
  assert.equal(source(room("r", "a", { maxRuns: 3 }), "maxRuns"), "here");
  assert.equal(source(room("r", "a", { maxRuns: 3 }), "maxWorkers"), "global");
  assert.equal(source(room("r", "a", { merge: false }), "merge"), "here");
  assert.equal(source(global({ start: "leads" }), "start"), "here");
  assert.equal(source(global({ start: "leads" }), "timeout"), "default");
  assert.equal(source(global({ profiles: [] }), "profiles"), "default");
});

test("a value reads as the owner says it: no limit for 0, every profile for none, the engine's choice for no size, time in words", () => {
  const e: WorkflowEffective = { ...defaults, start: "leads", maxRuns: 3, maxWorkers: 0, profiles: ["fast", "deep"], size: "large", timeout: 5400, worktree: false };
  assert.deepEqual(order.map(setting => valueText(e, setting)),
    ["leads", "3", "no limit", "fast, deep", "large", "90 minutes", "off", "on", "on", "on", "on", "on"]);
  assert.equal(valueText(defaults, "start"), "any session");
  assert.equal(valueText({ ...defaults, start: "root" }, "start"), "root only");
  assert.equal(valueText({ ...defaults, start: "off" }, "start"), "off");
  assert.equal(valueText(defaults, "profiles"), "all profiles");
  assert.equal(valueText({ ...defaults, profiles: [] }, "profiles"), "all profiles");
  assert.equal(valueText(defaults, "size"), "the engine's choice");
  assert.equal(valueText({ ...defaults, maxRuns: 10000 }, "maxRuns"), "10,000");
});

test("a timeout reads in the largest whole unit", () => {
  assert.equal(duration(18000), "5 hours");
  assert.equal(duration(3600), "1 hour");
  assert.equal(duration(2592000), "30 days");
  assert.equal(duration(86400), "1 day");
  assert.equal(duration(5400), "90 minutes");
  assert.equal(duration(60), "1 minute");
  assert.equal(duration(90), "90 seconds");
  assert.equal(duration(1), "1 second");
  assert.equal(duration(100000), "100,000 seconds");
});

test("a row names the values its scope sets, or that a room uses the global default, or that it is the built-in default", () => {
  assert.equal(scopeLine(room("r", "a"), order), "Uses global");
  assert.equal(scopeLine(global(), order), "Built-in defaults");
  assert.equal(scopeLine(room("r", "a", { worktree: false, maxRuns: 3 }, { worktree: false, maxRuns: 3 }), order), "Runs at once 3 · Worktrees off");
  assert.equal(scopeLine(global({ start: "root", timeout: 3600 }, { start: "root", timeout: 3600 }), order), "Who may start root only · Worker timeout 1 hour");
  assert.equal(scopeLine(room("r", "a", { profiles: ["fast"] }, { profiles: ["fast"] }), order), "Profiles fast");
});

test("Overrides counts the rooms that set a value and Uses global the rooms that set none; the global default is in neither", () => {
  const chips = workflowChips(order);
  assert.deepEqual(chips.map(chip => [chip.id, chip.label]), [["all", "All"], ["overrides", "Overrides"], ["inherits", "Uses global"]]);
  const [, over, inherits] = chips;
  const scopes = [global({ start: "leads" }), global(), room("r1", "a", { post: false }), room("r2", "b"), room("r3", "c", { profiles: [] })];
  assert.deepEqual(scopes.map(scope => over.test!(scope)), [false, false, true, false, false]);
  assert.deepEqual(scopes.map(scope => inherits.test!(scope)), [false, false, false, true, true]);
});

test("the summary counts the rooms, those with overrides and the values the global default sets", () => {
  assert.deepEqual(workflowSummary([global({ start: "leads", timeout: 60 }), room("r1", "a", { post: false }), room("r2", "b")], order), { rooms: 2, overriding: 1, global: 2 });
  assert.deepEqual(workflowSummary([global()], order), { rooms: 0, overriding: 0, global: 0 });
});

test("a change sets its one setting on the room, or with no room on the global default", () => {
  assert.deepEqual(setRequest(room("r1", "a"), { setting: "start", value: "root" }), { room: "r1", set: { start: "root" } });
  assert.deepEqual(setRequest(global(), { setting: "start", value: "off" }), { set: { start: "off" } });
  assert.equal("room" in setRequest(global(), { setting: "maxRuns", value: 2 }), false);
  assert.deepEqual(setRequest(room("r1", "a"), { setting: "maxRuns", value: 0 }), { room: "r1", set: { maxRuns: 0 } });
  assert.deepEqual(setRequest(room("r1", "a"), { setting: "maxWorkers", value: 8 }), { room: "r1", set: { maxWorkers: 8 } });
  assert.deepEqual(setRequest(room("r1", "a"), { setting: "timeout", value: 60 }), { room: "r1", set: { timeout: 60 } });
  assert.deepEqual(setRequest(room("r1", "a"), { setting: "size", value: "small" }), { room: "r1", set: { size: "small" } });
  assert.deepEqual(setRequest(room("r1", "a"), { setting: "profiles", value: ["fast"] }), { room: "r1", set: { profiles: ["fast"] } });
  assert.deepEqual(setRequest(global(), { setting: "worktree", value: false }), { set: { worktree: false } });
  assert.deepEqual(setRequest(room("r1", "a"), { setting: "resume", value: true }), { room: "r1", set: { resume: true } });
});

test("Use global and Use default clear the one setting on that scope", () => {
  assert.deepEqual(clearRequest(room("r1", "a"), "maxRuns"), { room: "r1", set: {}, clear: ["maxRuns"] });
  assert.deepEqual(clearRequest(global(), "post"), { set: {}, clear: ["post"] });
  assert.equal("room" in clearRequest(global(), "post"), false);
});

test("a number field sends a whole number unlike the value in force, the caps left to the engine, and anything else is put back", () => {
  const r = room("r1", "a", {}, { maxRuns: 3 });
  assert.deepEqual(numberChange(r, "maxRuns", "4"), { room: "r1", set: { maxRuns: 4 } });
  assert.deepEqual(numberChange(r, "maxRuns", " 0 "), { room: "r1", set: { maxRuns: 0 } });
  // The engine refuses these, in its words, under the field.
  assert.deepEqual(numberChange(r, "maxRuns", "10001"), { room: "r1", set: { maxRuns: 10001 } });
  assert.deepEqual(numberChange(r, "maxRuns", "-1"), { room: "r1", set: { maxRuns: -1 } });
  assert.deepEqual(numberChange(global(), "timeout", "2592001"), { set: { timeout: 2592001 } });
  assert.deepEqual(numberChange(global(), "maxWorkers", "12"), { set: { maxWorkers: 12 } });
  for (const text of ["3", "03", "", "  ", "2.5", "abc", "Infinity", "1e400"]) assert.equal(numberChange(r, "maxRuns", text), undefined, text);
  assert.equal(numberChange(global(), "timeout", "18000"), undefined);
});

test("Profiles start from the profiles in force, every one when none is set, and the last one unticked returns to inheriting", () => {
  const listed = ["deep", "fast", "cheap"];
  // From every profile: unticking one sets the rest, in the listed order.
  assert.deepEqual(profilesChange(room("r1", "a"), listed, "fast", false), { room: "r1", set: { profiles: ["deep", "cheap"] } });
  // From the global default's: the room starts from them.
  assert.deepEqual(profilesChange(room("r1", "a", {}, { profiles: ["fast"] }), listed, "deep", true), { room: "r1", set: { profiles: ["deep", "fast"] } });
  assert.deepEqual(profilesChange(global({ profiles: ["cheap"] }, { profiles: ["cheap"] }), listed, "deep", true), { set: { profiles: ["deep", "cheap"] } });
  // A set profile that profile.list no longer has stays, after the listed ones.
  assert.deepEqual(profilesChange(room("r1", "a", { profiles: ["gone", "fast"] }, { profiles: ["gone", "fast"] }), listed, "deep", true), { room: "r1", set: { profiles: ["deep", "fast", "gone"] } });
  // The last one unticked: a set value clears; an inherited one has nothing to clear, so the box is put back.
  assert.deepEqual(profilesChange(room("r1", "a", { profiles: ["fast"] }, { profiles: ["fast"] }), listed, "fast", false), { room: "r1", set: {}, clear: ["profiles"] });
  assert.equal(profilesChange(room("r1", "a", {}, { profiles: ["fast"] }), listed, "fast", false), undefined);
  // A tick on one already in force changes nothing.
  assert.equal(profilesChange(room("r1", "a"), listed, "fast", true), undefined);
});

test("the profile boxes are the listed profiles and any set profile no longer listed, ticked for those in force or all when none is set", () => {
  assert.deepEqual(profileChoices(["deep", "fast"], defaults), [{ name: "deep", on: true }, { name: "fast", on: true }]);
  assert.deepEqual(profileChoices(["deep", "fast"], { ...defaults, profiles: ["fast", "gone"] }), [{ name: "deep", on: false }, { name: "fast", on: true }, { name: "gone", on: true }]);
});

test("changes go one at a time in the order they were made, and a refused one does not stop the next", async () => {
  const run = serial();
  const seen: string[] = [];
  const later = (name: string, ms: number, fail = false) => () => new Promise<string>((resolve, reject) => setTimeout(() => { seen.push(`end ${name}`); if (fail) reject(new Error(name)); else resolve(name); }, ms));
  const first = run(() => { seen.push("start a"); return later("a", 30, true)(); });
  const second = run(() => { seen.push("start b"); return later("b", 1)(); });
  await assert.rejects(first, /a/);
  assert.equal(await second, "b");
  assert.deepEqual(seen, ["start a", "end a", "start b", "end b"]);
  assert.equal(run.pending(), 0);
});

test("a refusal shows under the setting the engine names, else the one the request sets or clears", () => {
  assert.equal(refusedSetting({ room: "r1", set: { maxRuns: 10001 } }), "maxRuns");
  assert.equal(refusedSetting({ set: { timeout: 0 } }), "timeout");
  assert.equal(refusedSetting({ set: { worktree: false } }), "worktree");
  assert.equal(refusedSetting({ set: { profiles: ["gone"] } }), "profiles");
  assert.equal(refusedSetting({ room: "r1", set: {}, clear: ["size"] }), "size");
  assert.equal(refusedSetting({ set: { start: "off" } }, "start"), "start");
  assert.equal(refusedSetting({ set: { maxRuns: 2 } }, "profiles"), "profiles");
  assert.equal(refusedSetting({ set: {} }), undefined);
});
