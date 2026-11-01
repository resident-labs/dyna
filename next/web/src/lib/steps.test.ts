import { test } from "node:test";
import assert from "node:assert/strict";
import { activityLabel, activeVerb, elapsed, stepTime, stepTitle, titleOf, workingGroup, workSummary } from "./steps.ts";
import type { TranscriptItem } from "./contract.gen.ts";

test("a live duration uses the injected clock", () => {
  assert.equal(elapsed("2026-01-01T00:00:00Z", Date.parse("2026-01-01T00:01:03Z")), "1 m 3 s");
});

test("summarizes adjacent tool kinds and shows a parsed target", () => {
  const items: TranscriptItem[] = [
    { id: "1", turn: "t", kind: "tool", at: "", tool: "bash", arguments: '{"command":"npm test"}' },
    { id: "2", turn: "t", kind: "tool", at: "", tool: "bash", arguments: "{" },
    { id: "3", turn: "t", kind: "tool", at: "", tool: "read", arguments: '{"path":"src/App.svelte"}' },
  ];
  assert.equal(workSummary(items), "Ran 2 commands, read 1 file");
  assert.equal(workingGroup(items.map(item => ({ ...item, ended: "2026-01-01T00:00:01Z" })), false), false);
  assert.equal(workingGroup(items, false), true);
  assert.equal(workingGroup(items, true), false);
  assert.equal(activeVerb("run", "Ran"), "Running");
  assert.equal(activeVerb("read", "Read"), "Reading");
  assert.equal(titleOf(items[0]).target, "npm test");
  assert.equal(titleOf(items[1]).target, "");
  assert.equal(titleOf({ ...items[2], arguments: '{"path":"reader.ts"}' }).target, "reader.ts");
});

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

test("an activity label never shows an MCP tool ID", () => {
  assert.equal(activityLabel("mcp__dyna__session"), "Session");
  assert.equal(activityLabel("mcp__shell__shell go test ./..."), "Ran go test ./...");
  assert.equal(activityLabel("read spec.md"), "read spec.md");
});
