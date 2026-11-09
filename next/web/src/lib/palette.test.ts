import { test } from "node:test";
import { strict as assert } from "node:assert";
import { build, escape, marks, matches, move, rows, settingsActions, showAll, type Sources } from "./palette.ts";
import { settingsPanes } from "./routes.ts";

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

test("settings actions use the pane names, targets and access rules from Settings", () => {
  const actions = settingsActions(true);
  assert.deepEqual(actions.map(action => action.id === "settings-pane" && [action.pane, action.label]),
    settingsPanes.map(pane => [pane.id, pane.label]));
  assert.ok(actions.every(action => action.hint === "Settings" && action.icon === "gear" && action.keys.length === 0));
  assert.deepEqual(settingsActions(false).map(action => action.id === "settings-pane" && action.pane), ["appearance", "addons"]);
});

test("pane names are searchable actions, without exposing admin panes to non-admin devices", () => {
  for (const admin of [true, false]) {
    const sources: Sources = { rooms: [], sessions: [], actions: settingsActions(admin), recent: [], messages: { state: "done", posts: [], total: 0 } };
    for (const [query, panes] of [["acc", ["accounts", "devices"]], ["hosts", ["hosts"]], ["profiles", ["profiles"]]] as const) {
      for (const scope of [undefined, "actions"] as const) {
        const found = rows(build(query, scope, sources));
        assert.deepEqual(found.map(item => item.kind === "action" && item.action.id === "settings-pane" && item.action.pane), admin ? panes : []);
      }
    }
    assert.equal(rows(build("appearance", undefined, sources)).length, 1);
    assert.equal(rows(build("skills", undefined, sources)).length, 1);
  }
});
