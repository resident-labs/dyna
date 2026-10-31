import { test } from "node:test";
import { strict as assert } from "node:assert";
import { build, escape, marks, matches, move, rows, showAll, type Sources } from "./palette.ts";

test("word-prefix matching marks only prefixes and requires every word", () => {
  assert.equal(matches("DEP qa", "QA auto-deploys"), true);
  assert.equal(matches("ploy", "auto-deploys"), false);
  assert.equal(matches("deploy missing", "auto-deploys"), false);
  assert.deepEqual(marks("auto-deploys", "deploy"), [{ text: "auto-", hit: false }, { text: "deploy", hit: true }, { text: "s", hit: false }]);
});

test("closed sessions follow open sessions and show-all is a keyboard row", () => {
  const sessions = Array.from({ length: 4 }, (_, i) => ({ id: String(i), name: `qa-${i}`, room: "r", roomName: "room", root: false, closed: i === 0, working: false }));
  const sources: Sources = { rooms: [], sessions, actions: [], recent: [], messages: { state: "done", posts: [], total: 0 } };
  const found = rows(build("qa", undefined, sources));
  assert.deepEqual(found.map(item => item.kind === "session" ? item.session.id : item.kind), ["1", "2", "3", "more"]);
  assert.equal(move(2, found.length, "ArrowDown"), 3);
  assert.deepEqual(escape({ scope: "sessions", text: "qa" }), { mode: { text: "qa" }, close: false });
});

test("session search identifies results beyond the first 50", () => {
  const sessions = Array.from({ length: 50 }, (_, i) => ({ id: String(i), name: `qa-${i}`, room: "r", roomName: "room", root: false, closed: false, working: false }));
  const sources: Sources = { rooms: [], sessions, sessionsTotal: 61, actions: [], recent: [], messages: { state: "done", posts: [], total: 0 } };
  const [section] = build("qa", "sessions", sources);
  assert.equal(section.shown, 50);
  assert.equal(section.total, 61);
  assert.equal(showAll("sessions", 61), "Show first 50 of 61 sessions");
});
