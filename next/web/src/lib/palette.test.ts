import { test } from "node:test";
import { strict as assert } from "node:assert";
import { build, escape, marks, matches, move, rows, type Sources } from "./palette.ts";

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
