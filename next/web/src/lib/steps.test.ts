import { test } from "node:test";
import assert from "node:assert/strict";
import { stepTime, stepTitle } from "./steps.ts";

test("a step reads as what it did, and no tool ID reaches the row", () => {
  assert.deepEqual(stepTitle({ tool: "bash", label: "bash go test ./..." }), { kind: "run", verb: "Ran", target: "go test ./...", full: "go test ./..." });
  assert.deepEqual(stepTitle({ tool: "mcp__dyna__take_snapshot", label: "mcp__dyna__take_snapshot" }), { kind: "tool", verb: "Take snapshot", target: "", full: "" });
});

test("a step's time reads in ms, then seconds, then minutes, and an unfinished step has none", () => {
  const started = "2026-10-31T00:00:00.000Z";
  assert.equal(stepTime({ started, ended: "2026-10-31T00:00:00.042Z" }), "42 ms");
  assert.equal(stepTime({ started, ended: "2026-10-31T00:00:02.360Z" }), "2.4 s");
  assert.equal(stepTime({ started, ended: "2026-10-31T00:01:36.400Z" }), "1 min 36 s");
  assert.equal(stepTime({ started, ended: null }), "");
});
