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
  const line = (rows: ReturnType<typeof holdRows>) => rows.map(row => `${row.label} ${row.count}`);
  assert.deepEqual(holdRows([]), []);
  assert.deepEqual(holdRows([needs("turn", "turn_1"), needs("shell_job", "job_7f3b"), owes("goal", "g1")]), [], "the thread, the header and the goals show these");
  assert.deepEqual(line(holdRows([needs("terminal"), owes("pr", "o/r#6")])), ["Terminal 1", "PR 1"]);
  assert.deepEqual(line(holdRows([owes("child", "a"), owes("pr", "o/r#6"), owes("child", "b")])), ["Child sessions 2", "PR 1"]);
  assert.deepEqual(line(holdRows([owes("child"), owes("child")])), ["Child sessions 2"]);
});

test("a row counts exactly the items it lists", () => {
  const owes = (kind: Responsibility["kind"], id?: string): Responsibility => ({ kind, group: "owes_work", id });
  const many = [...Array.from({ length: 11 }, (_, i) => owes("child", `c${i}`)), owes("pr", "o/r#1"), owes("pr", "bad id"), owes("question")];
  for (const row of holdRows(many)) assert.equal(row.count, row.items.length, row.kind);
  assert.deepEqual(holdRows(many).map(row => row.count), [11, 2, 1]);
});

test("only a row whose items have names opens a list", () => {
  const owes = (kind: Responsibility["kind"], id?: string): Responsibility => ({ kind, group: "owes_work", id });
  assert.deepEqual(holdRows([owes("terminal"), owes("question", "q1"), owes("child")]).map(row => [row.kind, row.opens]), [["terminal", false], ["question", true], ["child", false]]);
});

test("PRs and issues link to GitHub by their whole name", () => {
  const owes = (kind: Responsibility["kind"], id?: string): Responsibility => ({ kind, group: "owes_work", id });
  const [prs, issues] = holdRows([owes("pr", "o/r#6"), owes("pr", "o/r#7"), owes("issue", "o/r#8"), owes("issue", "o/s#9"), owes("issue", "not a ref")]);
  assert.deepEqual(prs.items, [{ label: "o/r#6", href: "https://github.com/o/r/pull/6", blocked: false }, { label: "o/r#7", href: "https://github.com/o/r/pull/7", blocked: false }]);
  assert.deepEqual(issues.items.map(item => [item.label, item.href]), [["o/r#8", "https://github.com/o/r/issues/8"], ["o/s#9", "https://github.com/o/s/issues/9"], ["not a ref", undefined]]);
});

test("a child links to its session, and one it cannot find stays text", () => {
  const owes = (id: string): Responsibility => ({ kind: "child", group: "owes_work", id });
  const href = (name: string) => name === "lead" ? "/r/room/s/ses_lead" : undefined;
  assert.deepEqual(holdRows([owes("lead"), owes("gone")], href)[0].items, [{ label: "lead", href: "/r/room/s/ses_lead", internal: true, blocked: false }, { label: "gone", blocked: false }]);
});

test("a kind with a blocked item says so", () => {
  const rows = holdRows([{ kind: "question", group: "owes_work", id: "q1" }, { kind: "question", group: "owes_work", id: "q2", blocked: true }]);
  assert.deepEqual(rows.map(row => [row.label, row.count, row.blocked, row.items.map(item => item.blocked)]), [["Questions", 2, true, [false, true]]]);
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
