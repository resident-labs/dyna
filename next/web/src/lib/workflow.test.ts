import { test } from "node:test";
import assert from "node:assert/strict";
import { decodeEvent } from "./event.ts";
import { feedRows } from "./feed.ts";
import { RunState } from "./run.svelte.ts";
import { LONG_PHASE, applyStep, branchLink, commitLink, failures, openable, phases, resultBlock, runLine, shownStep, stepTime, stepWord, tokens } from "./workflow.ts";
import type { Event, WorkflowStatusResponse, WorkflowStep } from "./contract.gen.ts";

const usage = { model: "", inputTokens: 0, outputTokens: 0, cacheReadTokens: 0, cacheWriteTokens: 0 };
const step = (key: string, fields: Partial<WorkflowStep> = {}): WorkflowStep => ({ key, kind: "agent", name: key, state: "done", usage, started: "2026-01-01T00:00:00Z", ended: "2026-01-01T00:00:04Z", transcript: `${key}.jsonl`, ...fields });
const status = (fields: Partial<WorkflowStatusResponse["run"]>, steps: WorkflowStep[] = [], merge?: WorkflowStatusResponse["merge"]): WorkflowStatusResponse =>
  ({ run: { run: "wf", room: "r", session: "crew", name: "sweep", profiles: ["p"], state: "running", failed: 0, usage, started: "2026-01-01T00:00:00Z", ...fields }, steps, merge });

function answering(replies: (Response | Promise<Response>)[], asked: unknown[] = []) {
  const original = globalThis.fetch;
  globalThis.fetch = ((_: RequestInfo | URL, init?: RequestInit) => {
    asked.push(JSON.parse(String(init?.body)));
    return Promise.resolve(replies.shift() ?? new Response("{}"));
  }) as typeof fetch;
  return () => { globalThis.fetch = original; };
}
const reply = (body: WorkflowStatusResponse) => new Response(JSON.stringify(body));

test("steps group by phase in first-run order, and phases that interleave stay whole", () => {
  const groups = phases([step("a", { phase: "Read" }), step("b", { phase: "Build" }), step("c", { phase: "Read", state: "running" }), step("d", { phase: "Build", state: "failed" }), step("e")]);
  assert.deepEqual(groups.map(group => [group.title, group.steps.map(item => item.key)]), [["Read", ["a", "c"]], ["Build", ["b", "d"]], ["Other steps", ["e"]]]);
  assert.deepEqual(groups.map(group => [group.done, group.running, group.failed]), [[1, 1, 0], [1, 0, 1], [1, 0, 0]]);
});

test("only a long phase folds, and a run with one phase never does", () => {
  const many = (phase: string, n: number) => Array.from({ length: n }, (_, i) => step(`${phase}${i}`, { phase }));
  const groups = phases([...many("Short", LONG_PHASE), ...many("Long", LONG_PHASE + 1)]);
  assert.deepEqual(groups.map(group => group.long), [false, true]);
  assert.deepEqual(phases(many("", 150)).map(group => [group.title, group.long, group.steps.length]), [["", false, 150]]);
  assert.deepEqual(phases(many("Only", 150)).map(group => group.long), [false]);
});

test("a live row replaces its step, a new one joins the end, and an older row never undoes a newer one", () => {
  const running = step("a", { state: "running", ended: undefined, transcript: undefined });
  let steps = applyStep([], running);
  steps = applyStep(steps, { ...running, transcript: "a.jsonl" });
  assert.equal(steps[0].transcript, "a.jsonl");
  const done = step("a", { usage: { ...usage, outputTokens: 5 } });
  steps = applyStep(steps, done);
  assert.equal(applyStep(steps, running), steps);
  assert.equal(applyStep(steps, { ...running, transcript: "a.jsonl" }), steps);
  const rerun = { ...running, started: "2026-01-01T00:10:00Z" };
  assert.deepEqual(applyStep(steps, rerun), [rerun]);
  assert.deepEqual(applyStep(steps, step("b")).map(item => item.key), ["a", "b"]);
});

test("a step's state, time and tokens say what it did, and nothing where it has nothing to say", () => {
  const waiting = step("a", { state: "running", ended: undefined, transcript: undefined });
  assert.deepEqual([stepWord(waiting), stepTime(waiting, Date.parse("2026-01-01T00:00:05Z")), tokens(waiting.usage)], [{ word: "Waiting" }, "", ""]);
  const running = { ...waiting, transcript: "a.jsonl" };
  assert.deepEqual([stepWord(running), stepTime(running, Date.parse("2026-01-01T00:01:30Z"))], [{ word: "Running", tone: "working" }, "1m 30s"]);
  assert.deepEqual(stepWord(step("c", { kind: "run", state: "running", transcript: undefined })), { word: "Running", tone: "working" });
  assert.deepEqual([stepWord(step("d")), stepTime(step("d"), 0)], [{ word: "Done" }, "4s"]);
  assert.equal(tokens({ ...usage, inputTokens: 4, outputTokens: 136, cacheReadTokens: 12311, cacheWriteTokens: 15018 }), "27.5k");
  assert.equal(tokens({ ...usage, outputTokens: 2_400_000 }), "2.4M");
});

test("only a worker with a file opens, and the address names it", () => {
  assert.equal(openable(step("a")), true);
  assert.equal(openable(step("b", { transcript: undefined, state: "running" })), false);
  assert.equal(openable(step("c", { kind: "run" })), false);
  assert.equal(shownStep("?step=abc"), "abc");
  assert.equal(shownStep(""), undefined);
});

test("a crew's row says it is a workflow and how it stands", () => {
  assert.equal(runLine({ state: "running" }), "Workflow · Running");
  assert.equal(runLine({ state: "running" }, true), "Workflow · Starting");
  assert.equal(runLine({ state: "stopped" }), "Workflow · Stopped");
});

test("a conflict shows in the merge section alone, and a result reads by what it is", () => {
  const conflict = step("c", { kind: "log", name: "merge conflict: x", state: "failed" });
  assert.deepEqual(failures([conflict, step("a", { state: "failed" }), step("b")]).map(item => item.key), ["a"]);
  assert.deepEqual(resultBlock(`{"found":2}`), { code: true, text: `{\n  "found": 2\n}` });
  assert.deepEqual(resultBlock("All done"), { code: false, text: "All done" });
  assert.deepEqual(resultBlock("42"), { code: false, text: "42" });
});

test("the run branch and commit link to the repository, and a run with none links nowhere", () => {
  assert.equal(branchLink("resident-labs/dyna", "v2/ses_1-run"), "https://github.com/resident-labs/dyna/tree/v2/ses_1-run");
  assert.equal(commitLink("resident-labs/dyna", "18b5fa2"), "https://github.com/resident-labs/dyna/commit/18b5fa2");
  assert.equal(branchLink(undefined, "x"), undefined);
});

test("a closed run shows its journal by session, with its result and merge", async () => {
  const asked: unknown[] = [];
  const merge = { branch: "v2/s-run", commit: "abc", merged: ["a"], conflicts: [{ step: "b", files: ["f.go"] }] };
  const restore = answering([reply(status({ state: "failed", result: "Error: boom", ended: "2026-01-01T00:01:00Z" }, [step("a"), step("b", { state: "failed", reason: "refused" })], merge))], asked);
  try {
    const run = new RunState("crew");
    await run.read();
    const { status: read, steps, failed } = run.snapshot;
    assert.deepEqual(asked, [{ session: "crew" }]);
    assert.deepEqual([read?.run.state, read?.run.result, read?.merge?.conflicts?.[0].files, steps.length, failed], ["failed", "Error: boom", ["f.go"], 2, false]);
  } finally { restore(); }
});

test("a run that did not load says so, and a later read recovers", async () => {
  const restore = answering([new Response("{}", { status: 503 }), reply(status({}, [step("a")]))]);
  try {
    const run = new RunState("crew");
    await run.read();
    assert.equal(run.snapshot.failed, true);
    await run.read();
    assert.deepEqual([run.snapshot.failed, run.snapshot.steps.length], [false, 1]);
  } finally { restore(); }
});

test("rows that arrive during a read go on top of it, and no row means read again", async () => {
  let answer!: (response: Response) => void;
  const restore = answering([new Promise<Response>(done => { answer = done; }), reply(status({ state: "done" }, [step("a"), step("b")]))]);
  try {
    const run = new RunState("crew");
    const reading = run.read();
    run.apply(step("b", { state: "running", ended: undefined }));
    assert.equal(run.snapshot.steps.length, 0);
    answer(reply(status({}, [step("a")])));
    await reading;
    assert.deepEqual(run.snapshot.steps.map(item => item.key), ["a", "b"]);
    run.apply(step("c"));
    assert.deepEqual(run.snapshot.steps.map(item => item.key), ["a", "b", "c"]);
    run.apply();
    await new Promise(done => setTimeout(done));
    assert.deepEqual([run.snapshot.status?.run.state, run.snapshot.steps.length], ["done", 2]);
  } finally { restore(); }
});

test("a step change decodes with seq 0 and adds no row to any conversation", () => {
  const change = { seq: 0, id: "e", at: "2026-01-01T00:00:00Z", from: { kind: "engine" }, type: "workflow.step_changed", body: { run: "wf", session: "crew", step: step("a") } };
  const decoded = decodeEvent(change);
  assert.ok(decoded);
  assert.equal(decodeEvent({ ...change, body: { run: "wf", session: "crew", step: { key: "a" } } }), undefined);
  assert.deepEqual(feedRows([{ ...(decoded as Event), seq: 1 }], { kind: "feed" }).filter(row => row.kind !== "day"), []);
});
