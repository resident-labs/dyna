import assert from "node:assert/strict";
import { test } from "node:test";
import type { RoomSummary } from "./contract.gen.ts";
import { writeGroups, writeLabel, writeOn, writeRequest, writesRepos } from "./githubwrites.ts";

const room = (githubWrites?: RoomSummary["githubWrites"]) => ({ id: "room_1", name: "horizon", githubWrites }) as RoomSummary;

test("each switch is labelled by its group and its room", () => {
  assert.equal(writeLabel("issues", room()), "GitHub writes: Issues in horizon");
  assert.equal(writeLabel("pulls", room()), "GitHub writes: Pull requests in horizon");
  assert.equal(writeLabel("projects", room()), "GitHub writes: Projects in horizon");
  for (const group of ["issues", "pulls", "projects", "actions"] as const) assert.doesNotMatch(writeLabel(group, room()), /:\s+in |undefined|\$\{/);
});

test("a room with nothing set is off, and its repositories read as all of the room's", () => {
  assert.equal(writeOn(room(), "issues"), false);
  assert.equal(writeOn(room(null), "pulls"), false);
  assert.equal(writesRepos(room()), "all of this room's repositories");
  assert.equal(writesRepos(room({ repos: ["*"], groups: ["issues"] })), "all of this room's repositories");
  assert.equal(writesRepos(room({ repos: ["a/b", "a/c"], groups: ["issues"] })), "a/b, a/c");
});

test("turning a group on or off keeps the others and the repositories", () => {
  assert.deepEqual(writeRequest(room(), "issues", true), { room: "room_1", githubWrites: { repos: ["*"], groups: ["issues"] } });
  const set = room({ repos: ["a/b"], groups: ["projects", "issues"] });
  assert.deepEqual(writeRequest(set, "pulls", true), { room: "room_1", githubWrites: { repos: ["a/b"], groups: ["issues", "pulls", "projects"] } });
  assert.deepEqual(writeRequest(set, "issues", false), { room: "room_1", githubWrites: { repos: ["a/b"], groups: ["projects"] } });
  assert.deepEqual(writeRequest(room({ repos: ["a/b"], groups: ["issues"] }), "issues", false).githubWrites?.groups, []);
});

test("Actions is opt-in and preserves existing groups", () => {
  assert.equal(writeOn(room(), "actions"), false);
  assert.equal(writeLabel("actions", room()), "GitHub writes: Actions in horizon");
  const existing = room({ repos: ["a/b"], groups: ["issues", "pulls", "projects"] });
  assert.deepEqual(writeRequest(existing, "actions", true).githubWrites, { repos: ["a/b"], groups: ["issues", "pulls", "projects", "actions"] });
});

test("Pull requests help describes inherited ownership and lifecycle writes", () => {
  assert.equal(writeGroups.find(each => each.group === "pulls")?.note, "Comment, review without approving, and edit a PR's title, body and labels; mark it ready or draft; close, reopen or retarget PRs a session or those below it own. Never merge or approve.");
});
