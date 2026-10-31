import { test } from "node:test";
import assert from "node:assert/strict";
import { failingLine, jobGroups } from "./jobs.ts";

test("jobs group by session, running then failed then passed, newest first, and a failure opens at its first failing line", () => {
  const job = (id: string, running: boolean, exitCode = 0) => ({ job: id, command: id, running, exitCode, text: "", started: "" });
  const groups = jobGroups([
    { id: "calm", jobs: [job("build", false)] },
    { id: "busy", jobs: [job("old", false), job("lint", false, 1), job("test", true), job("new", false)] },
    { id: "none", jobs: [] }]);
  assert.deepEqual(groups.map(group => [group.session, ...group.jobs.map(j => j.job)]), [["busy", "test", "lint", "new", "old"], ["calm", "build"]]);
  const text = "=== RUN   TestA\n--- FAIL: TestA (0.01s)\nFAIL\n";
  const at = failingLine(text);
  assert.equal(at && text.slice(at.start, at.end), "--- FAIL: TestA (0.01s)");
  assert.equal(failingLine("ok\n"), undefined);
});
