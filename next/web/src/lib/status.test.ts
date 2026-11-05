import { test } from "node:test";
import assert from "node:assert/strict";
import { statusOf, countOf, urgency } from "./status.ts";

test("background waits do not reuse urgent owner waiting or mask work and failure", () => {
  const facts = { broken: false, waiting: false, working: false, failed: false, background: true };
  assert.equal(statusOf(facts), "background");
  assert.equal(statusOf({ ...facts, working: true }), "working");
  assert.equal(statusOf({ ...facts, failed: true }), "failed");
  assert.equal(statusOf({ ...facts, waiting: true, working: true }), "waiting");
  assert.equal(statusOf({ ...facts, broken: true, waiting: true }), "failed");
  assert.ok(urgency("waiting") < urgency("background"));
  assert.equal(countOf(["background", "waiting", "working"]).background, 1);
});
