import { test } from "node:test";
import assert from "node:assert/strict";
import { decodeShellOutput } from "./event.ts";
import { endCause, failed, failingLine, jobGroups, jobTime, jobTitle } from "./jobs.ts";

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

test("a job's time reads in seconds, minutes or hours", () => {
  const job = (seconds: number) => ({ job: "j", command: "j", running: false, exitCode: 0, text: "",
    started: "2026-10-31T10:00:00Z", ended: new Date(Date.parse("2026-10-31T10:00:00Z") + seconds * 1000).toISOString() });
  assert.deepEqual([38, 120, 3900].map(seconds => jobTime(job(seconds))), ["38 s", "2 min", "1 h 5 min"]);
});

test("only the newest three passed jobs show; a failed job stays in view, and the rest wait behind Show older", () => {
  const job = (id: string, exitCode = 0) => ({ job: id, command: id, running: false, exitCode, text: "", started: "" });
  const [group] = jobGroups([{ id: "s", jobs: [job("a"), job("b"), job("broke", 1), job("c"), job("d"), job("e")] }]);
  assert.deepEqual([group.jobs.map(j => j.job), group.older.map(j => j.job)], [["broke", "e", "d", "c"], ["b", "a"]]);
});

// LEGACY(v1-transfer): the cases with no end field in this test, the exit-code fallback. Remove after the transfer.
test("an end cause reads from the end field, and an older event falls back on its exit code", () => {
  const base = { exitCode: 0 };
  assert.deepEqual([{ ...base, exitCode: 2, end: { kind: "exit", code: 2 } }, { ...base, exitCode: -1, end: { kind: "signal", signal: "SIGTERM" } }, { ...base, exitCode: -1, end: { kind: "stopped" } },
    { exitCode: 0 }, { exitCode: 3 }, { exitCode: -1 }].map(end => [endCause(end as never, "lead"), failed(end as never)]),
    [["Exit 2", true], ["Ended by a signal (SIGTERM)", true], ["Stopped by lead", false], ["Passed", false], ["Exit 3", true], ["Ended by a signal", true]]);
  assert.equal(jobTitle({ job: "j", command: "make test\nmake lint", description: "", running: false, exitCode: 0, text: "", started: "" }), "make test");
});

test("the output decoder keeps the description and end, and still takes a message without them", () => {
  const message = { job: "j", command: "c", running: false, exitCode: -1, text: "", started: "t" };
  assert.deepEqual(decodeShellOutput({ ...message, description: "Build", end: { kind: "signal", signal: "SIGTERM" } }),
    { ...message, description: "Build", end: { kind: "signal", signal: "SIGTERM" } });
  assert.deepEqual(decodeShellOutput(message), message);
  assert.equal(decodeShellOutput({ ...message, end: { kind: "other" } }), undefined);
});
