import { test } from "node:test";
import { strict as assert } from "node:assert";
import { closeNote, descendants, handNote, holdRows, sessionActions, sessionTree } from "./session-tree.ts";
import type { Responsibility, SessionStarted } from "./contract.gen.ts";
import type { Status } from "./status.ts";

test("a session's actions follow what the engine takes", () => {
  assert.deepEqual(sessionActions(undefined, false), []);
  assert.deepEqual(sessionActions(undefined, true, "failed"), ["stop", "restart"]);
  assert.deepEqual(sessionActions("root", false, "ready"), ["move", "close"]);
  assert.deepEqual(sessionActions("root", true, "failed"), ["move", "stop", "restart", "close"]);
});

test("the list holds what shows nowhere else, one row per kind", () => {
  const needs = (kind: Responsibility["kind"], id?: string): Responsibility => ({ kind, group: "needs_box", id });
  const owes = (kind: Responsibility["kind"], id?: string, blocked?: boolean): Responsibility => ({ kind, group: "owes_work", id, blocked });
  assert.deepEqual(holdRows([]), []);
  assert.deepEqual(holdRows([needs("turn", "turn_1"), needs("shell_job", "job_7f3b"), owes("goal", "g1")]), [], "the thread, the header and the goals show these");
  assert.deepEqual(holdRows([needs("terminal"), owes("pr", "o/r#6")]).map(row => row.text), ["Terminal", "PR o/r#6"]);
  assert.deepEqual(holdRows([owes("child", "a"), owes("pr", "o/r#6"), owes("child", "b")]).map(row => row.text), ["2 child sessions: a, b", "PR o/r#6"]);
  assert.deepEqual(holdRows([owes("child"), owes("child")]).map(row => row.text), ["2 child sessions"]);
});

test("a kind with a blocked item says so", () => {
  const rows = holdRows([{ kind: "question", group: "owes_work", id: "q1" }, { kind: "question", group: "owes_work", id: "q2", blocked: true }]);
  assert.deepEqual(rows.map(row => [row.text, row.blocked]), [["2 questions: q1, q2", true]]);
});

test("a close counts children as sessions", () => {
  assert.equal(handNote([{ kind: "child", group: "owes_work", id: "a" }, { kind: "child", group: "owes_work", id: "b" }], "root"), "This hands 2 child sessions to root.");
});

test("a close says what it hands over", () => {
  const goal = { kind: "goal", group: "owes_work", id: "g1" } as const, pr = { kind: "pr", group: "owes_work", id: "o/r#1" } as const, issue = { kind: "issue", group: "owes_work", id: "o/r#2" } as const;
  assert.equal(handNote([], "root"), "");
  assert.equal(handNote([pr], "lead"), "This hands 1 PR to lead.");
  assert.equal(handNote([goal, goal, pr], "root"), "This hands 2 goals and 1 PR to root.");
  assert.equal(handNote([goal, pr, issue], "root"), "This hands 1 goal, 1 PR and 1 issue to root.");
});

test("a close names what it takes along", () => {
  const item = (session: string, parent?: string): SessionStarted => ({ session, parent, name: session, profile: "p" });
  assert.deepEqual(descendants([item("root"), item("a", "root"), item("b", "a"), item("c", "root")], "root"), ["a", "c", "b"]);
  assert.equal(closeNote([]), "Its work is archived first.");
  assert.equal(closeNote(["a", "b"]), "Also closes a and b. Their work is archived first.");
  assert.equal(closeNote(["a", "b", "c"]), "Also closes a, b and 1 more. Their work is archived first.");
});

test("tree keeps focus visible and reports urgent folded descendants", () => {
  const item = (session: string, parent?: string): SessionStarted => ({ session, parent, name: session, profile: "p" });
  const sessions = [item("root"), item("a", "root"), item("b", "a"), item("c", "root"), item("d", "root"), item("orphan", "missing")];
  const own = new Map<string, Status>(sessions.map(s => [s.session, s.session === "b" ? "waiting" : "idle"]));
  const rows = sessionTree(sessions, own, () => false, "c", new Set(["a"]), new Set());
  assert.deepEqual(rows.map(row => row.kind === "idle" ? [row.kind, row.count] : [row.session.session, row.depth, row.hidden, row.dot]), [
    ["root", 0, 0, "idle"], ["a", 1, 1, "waiting"], ["c", 1, 0, "idle"], ["d", 1, 0, "idle"], ["orphan", 0, 0, "idle"],
  ]);
  const grouped = sessionTree(sessions, own, () => false, undefined, new Set(), new Set());
  assert.ok(grouped.some(row => row.kind === "idle" && row.count === 2 && !row.open));
  const starting = sessionTree(sessions, own, id => id === "d", undefined, new Set(), new Set());
  assert.ok(starting.some(row => row.kind === "session" && row.session.session === "d"));
});
