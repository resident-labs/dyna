import { test } from "node:test";
import { strict as assert } from "node:assert";
import { closeNote, descendants, handNote, holdsText, sessionActions, sessionTree } from "./session-tree.ts";
import type { SessionStarted } from "./contract.gen.ts";
import type { Status } from "./status.ts";

test("a session's actions follow what the engine takes", () => {
  assert.deepEqual(sessionActions(undefined, false), []);
  assert.deepEqual(sessionActions(undefined, true, "failed"), ["stop", "restart"]);
  assert.deepEqual(sessionActions("root", false, "ready"), ["move", "close"]);
  assert.deepEqual(sessionActions("root", true, "failed"), ["move", "stop", "restart", "close"]);
});

test("the list reads as the engine sends it, without the turn that has its own row", () => {
  assert.equal(holdsText([]), "");
  assert.equal(holdsText([{ kind: "turn", group: "needs_box", id: "turn_1" }, { kind: "shell_job", group: "needs_box", id: "job_7f3b" }, { kind: "terminal", group: "needs_box" }, { kind: "pr", group: "owes_work", id: "o/r#6" }]), "shell job job_7f3b, terminal, PR o/r#6");
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
