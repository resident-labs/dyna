import { test } from "node:test";
import assert from "node:assert/strict";
import { KEEP, KEEP_FINISHED, applyOutput, watchShell, type ShellConnection, type ShellJob } from "./shell.ts";

test("resets replace, other messages append, and kept text stays under the cap at a line start", () => {
  const jobs = new Map<string, ShellJob>();
  const job = { job: "j", command: "npm run dev", running: true, exitCode: 0 };
  applyOutput(jobs, { ...job, text: "ready\n", reset: true });
  applyOutput(jobs, { ...job, text: "built\n" });
  assert.equal(jobs.get("j")?.text, "ready\nbuilt\n");
  applyOutput(jobs, { ...job, text: `${"a".repeat(KEEP - 2)}\nlast\n` });
  assert.equal(jobs.get("j")?.text, "last\n");
  // After a reconnect the engine starts a new generation, whose first message resets each job.
  applyOutput(jobs, { ...job, running: false, exitCode: 1, text: "ready\nFAIL\n", reset: true });
  assert.deepEqual(jobs.get("j"), { ...job, running: false, exitCode: 1, text: "ready\nFAIL\n" });
});

test("a reconnect starts empty, so a running job that finished and was pruned meanwhile leaves the view", () => {
  const jobs = new Map<string, ShellJob>();
  const source: ShellConnection = { onopen: null, onmessage: null, onerror: null, readyState: 1, close: () => {} };
  watchShell("s", jobs, () => {}, () => source);
  const send = (job: string) => source.onmessage?.(new MessageEvent("message", { data: JSON.stringify({ job, command: "sleep 60", running: true, exitCode: 0, text: "", reset: true }) }));
  source.onopen?.(new Event("open"));
  send("old");
  source.onopen?.(new Event("open"));
  send("new");
  assert.deepEqual([...jobs.keys()], ["new"]);
});

test("keeps running jobs and only the newest finished jobs, as the box does", () => {
  const jobs = new Map<string, ShellJob>();
  applyOutput(jobs, { job: "server", command: "npm run dev", running: true, exitCode: 0, text: "" });
  for (let i = 0; i < KEEP_FINISHED + 2; i++) applyOutput(jobs, { job: `t${i}`, command: "go test", running: false, exitCode: 0, text: "ok\n" });
  assert.deepEqual([...jobs.keys()], ["server", ...Array.from({ length: KEEP_FINISHED }, (_, i) => `t${i + 2}`)]);
});

test("a job that ends moves behind the jobs that ended before it", () => {
  const jobs = new Map<string, ShellJob>();
  const send = (job: string, running: boolean) => applyOutput(jobs, { job, command: job, running, exitCode: 0, text: "" });
  send("server", true);
  send("tests", false);
  send("server", false);
  assert.deepEqual([...jobs.keys()], ["tests", "server"]);
});
