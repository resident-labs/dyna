import { test } from "node:test";
import assert from "node:assert/strict";
import { failingLine, jobGroups, jobTime } from "./jobs.ts";

test("jobs group by session, running then failed then passed, newest first, and a failure opens at its first failing line", () => {
  const job = (id: string, running: boolean, exitCode = 0) => ({ job: id, command: id, running, exitCode, text: "", started: "" });
  const groups = jobGroups([
    { id: "calm", jobs: [job("build", false)] },
    { id: "busy", jobs: [job("old", false), job("lint", false, 1), job("test", true), job("new", false)] },
    { id: "none", jobs: [] }], 0);
  assert.deepEqual(groups.map(group => [group.session, ...group.jobs.map(j => j.job)]), [["busy", "test", "lint", "new", "old"], ["calm", "build"]]);
  const text = "=== RUN   TestA\n--- FAIL: TestA (0.01s)\nFAIL\n";
  const at = failingLine(text);
  assert.equal(at && text.slice(at.start, at.end), "--- FAIL: TestA (0.01s)");
  assert.equal(failingLine("ok\n"), undefined);
});

test("a job's time reads in seconds, minutes or hours, and passed jobs over an hour old fold", () => {
  const job = (id: string, exitCode: number, seconds: number) => ({ job: id, command: id, running: false, exitCode, text: "",
    started: "2026-10-31T10:00:00Z", ended: new Date(Date.parse("2026-10-31T10:00:00Z") + seconds * 1000).toISOString() });
  assert.deepEqual([38, 120, 3900].map(seconds => jobTime(job("", 0, seconds))), ["38 s", "2 min", "1 h 5 min"]);
  const [group] = jobGroups([{ id: "s", jobs: [job("old", 0, 0), job("broke", 1, 0), job("new", 0, 3000)] }], Date.parse("2026-10-31T11:30:00Z"));
  assert.deepEqual([group.jobs.map(j => j.job), group.older.map(j => j.job)], [["broke", "new"], ["old"]]);
});
