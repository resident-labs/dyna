import { test } from "node:test";
import assert from "node:assert/strict";
import { savedBy } from "./instruction-saved.ts";

test("an instructions save names the root that made it, with its room", () => {
  assert.equal(savedBy({ kind: "session", session: "ses_a", name: "root", roomName: "horizon" }), "by root (horizon)");
});

test("an owner's save reads as yours, and an unrecorded one names nobody", () => {
  assert.equal(savedBy({ kind: "owner" }), "by you");
  assert.equal(savedBy(undefined), "");
  assert.equal(savedBy(null), "");
});
