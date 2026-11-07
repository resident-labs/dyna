import { test } from "node:test";
import assert from "node:assert/strict";
import { profileCommands, parsePrefix, commandProblem, sessionName, type CommandContext } from "./composer-commands.ts";
import type { ProfileSummary } from "./profiles.ts";
const commands = profileCommands([{ name: "sol", taste: 7, intelligence: 9, cost: 8 } as ProfileSummary]);
test("start-only parser, known, unknown, escapes and reserved slash", () => {
  assert.equal(parsePrefix("!sol task", commands)?.command?.name, "sol");
  assert.equal(commandProblem("!missing task", commands, false), "No profile named missing");
  for (const text of ["text !sol task", " !sol task", "!!sol task", "! task", "!\ttask", "/sol task"]) assert.equal(parsePrefix(text, commands), undefined, text);
  assert.equal(commandProblem("!sol", commands, false), "Add what the new session should do");
});
function context(dm: boolean, fail = false) {
  const calls: unknown[] = [];
  const context: CommandContext = { dm, start: async (profile, brief) => { calls.push({ room: "r", profile, brief, name: sessionName(brief, ["fix-the-bug"]) }); }, switchProfile: async profile => { calls.push(["switch", profile]); if (fail) throw Error("refused"); }, send: text => { calls.push(["send", text]); }, clear: () => { calls.push("clear"); } };
  return { calls, context };
}
test("room start fields and deduplicated valid name", async () => {
  const c = context(false); await commands[0].run("Fix the bug", c.context);
  assert.deepEqual(c.calls, [{ room: "r", profile: "sol", brief: "Fix the bug", name: "fix-the-bug-2" }, "clear"]);
  assert.equal(sessionName("root", []), "root-2");
});
test("DM permanently switches before sending", async () => {
  const c = context(true); await commands[0].run("hello", c.context);
  assert.deepEqual(c.calls, [["switch", "sol"], ["send", "hello"]]);
});
test("DM switch only", async () => {
  const c = context(true); await commands[0].run("", c.context);
  assert.deepEqual(c.calls, [["switch", "sol"], "clear"]);
});
test("failed switch sends nothing", async () => {
  const c = context(true, true); await assert.rejects(commands[0].run("hello", c.context));
  assert.deepEqual(c.calls, [["switch", "sol"]]);
});
