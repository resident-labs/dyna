import test from "node:test";
import assert from "node:assert/strict";
import { foldOpen, isLiveTurn, workStepSummary } from "./work-steps.ts";

test("turn steps default closed except failures and keep explicit toggles", () => {
  assert.equal(foldOpen(false, false), false);
  assert.equal(foldOpen(true, false), true);
  assert.equal(foldOpen(false, true), true);
  assert.equal(foldOpen(true, true), false);
  assert.equal(isLiveTurn("t2", "t2", "t2"), true);
  assert.equal(isLiveTurn("t1", "t2", "t2"), false);
  assert.equal(isLiveTurn("t2", "t2"), false);
  assert.equal(workStepSummary("2026-01-01T00:00:00Z", "2026-01-01T00:01:12Z"), "Worked 1 min 12 s · Show steps");
  assert.equal(workStepSummary("2026-01-01T00:00:00Z", "2026-01-01T00:01:12Z", 1), "Worked 1 min 12 s · 1 step");
  assert.equal(workStepSummary("2026-01-01T00:00:00Z", "2026-01-01T00:00:02Z", 2), "Worked 2.0 s · 2 steps");
});
