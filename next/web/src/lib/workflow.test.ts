import { test } from "node:test";
import assert from "node:assert/strict";
import { decodeEvent } from "./event.ts";
import { feedRows } from "./feed.ts";
import { RunState } from "./run.svelte.ts";
import type { Json } from "./json.ts";
import { FAILED, LONG_PHASE, READABLE_SPAN, STRIP_PHASES, applyStep, branchLink, commitLink, countedLine, failedNodes, figures, flow, foldable, jsonFields, minutesLabel, nested, openable, phases, reading, roomRuns, runCounts, runRows, runScale, shownFacts, shownStep, spanReads, stageOf, stagesOf, stepBody, stepSpan, stepTime, stepTitle, stepWord, strip, timed, timeline, tokens } from "./workflow.ts";
import type { Event, WorkflowInfo, WorkflowStatusResponse, WorkflowStep } from "./contract.gen.ts";

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

test("a script that wrote no status gets a counted line", () => {
  assert.equal(countedLine({ steps: 12, running: 0, failed: 2 }), "12 steps · 2 failed");
  assert.equal(countedLine({ steps: 1, running: 0, failed: 0 }), "1 step");
  assert.equal(countedLine({ steps: 0, running: 0, failed: 0 }), "No steps yet");
});

test("a child sits under its workflow step and counts in its phase, and a child whose parent is gone stays at the top", () => {
  const steps = [step("wf", { kind: "workflow", phase: "Build" }), step("a", { parent: "wf", phase: "Inner" }), step("b", { parent: "a", state: "failed" }), step("lost", { parent: "kept-by-resume" }), step("c", { phase: "Build", state: "running" })];
  const tree = nested(steps);
  assert.deepEqual(tree.map(node => node.step.key), ["wf", "lost", "c"]);
  assert.deepEqual(tree[0].children.map(node => [node.step.key, node.children.map(child => child.step.key)]), [["a", ["b"]]]);
  const [build, other] = phases(steps);
  assert.deepEqual([build.title, build.nodes.map(node => node.step.key), build.steps.length, build.done, build.running, build.failed], ["Build", ["wf", "c"], 4, 2, 1, 1]);
  assert.deepEqual([other.title, other.steps.map(item => item.key)], ["Other steps", ["lost"]]);
  assert.deepEqual(failedNodes(build.nodes).map(node => [node.step.key, node.children[0].children[0].step.key]), [["wf", "b"]]);
});

test("the strip gives each phase its share and fills it with done, failed, then running", () => {
  const many = (phase: string, n: number, state: WorkflowStep["state"] = "done") => Array.from({ length: n }, (_, i) => step(`${phase}${state}${i}`, { phase, state }));
  assert.deepEqual(strip(phases([])), []);
  assert.deepEqual(strip(phases(many("", 3))), [{ key: "", title: "", share: 100, segments: [{ state: "done", share: 100 }] }]);
  const parts = strip(phases([...many("A", 15), ...many("A", 3, "failed"), ...many("A", 12, "running"), ...many("B", 10, "running")]));
  assert.deepEqual(parts.map(part => [part.title, part.share]), [["A", 75], ["B", 25]]);
  assert.deepEqual(parts[0].segments, [{ state: "done", share: 50 }, { state: "failed", share: 10 }, { state: "running", share: 40 }]);
  const huge = strip(phases([...many("A", 199), step("x", { phase: "A", state: "failed" })]));
  assert.equal(huge[0].segments.reduce((sum, segment) => sum + segment.share, 0), 100);
  assert.deepEqual(huge[0].segments.map(segment => segment.state), ["done", "failed"]);
  const crowded = Array.from({ length: STRIP_PHASES + 1 }, (_, i) => step(`p${i}`, { phase: `P${i}`, state: i ? "done" : "failed" }));
  assert.equal(strip(phases(crowded.slice(1))).length, STRIP_PHASES);
  assert.deepEqual(strip(phases(crowded)).map(part => [part.share, part.segments.map(segment => segment.state)]), [[100, ["done", "failed"]]]);
});

test("a step's span places it in the run, and a live run's scale moves by the minute", () => {
  const at = (s: number) => new Date(Date.UTC(2026, 0, 1, 0, 0, s)).toISOString();
  const scale = runScale({ started: at(0), ended: at(100) }, 0);
  assert.deepEqual(stepSpan(step("a", { started: at(10), ended: at(40) }), scale, 0), { left: 10, width: 30 });
  const live = (now: number) => runScale({ started: at(0) }, Date.parse(at(now)));
  assert.equal(live(10).to, live(50).to);
  assert.equal(live(61).to - live(50).to, 60_000);
  const running = stepSpan(step("b", { started: at(30), ended: undefined, state: "running" }), live(45), Date.parse(at(45)));
  assert.deepEqual(running, { left: 50, width: 25 });
});

test("a step opens in place only with more to show; a worker opens its transcript", () => {
  for (const kind of ["run", "log", "post"] as const) {
    assert.equal(foldable(step("more", { kind, transcript: undefined, more: true })), true, kind);
    assert.equal(foldable(step("one line", { kind, transcript: undefined })), false, kind);
  }
  assert.equal(foldable(step("w", { kind: "workflow", transcript: undefined, more: true })), false);
  assert.equal(foldable(step("a", { more: true })), false);
  assert.equal(foldable(step("waiting", { transcript: undefined, state: "running", ended: undefined })), false);
  assert.equal(foldable(step("lost", { transcript: undefined, state: "failed", reason: "no place", more: true })), true);
});

test("a span too short to read as a bar does not draw, and a run with none has no column", () => {
  const at = (s: number) => new Date(Date.UTC(2026, 0, 1, 0, 0, s)).toISOString();
  const scale = runScale({ started: at(0), ended: at(100) }, 0);
  const short = step("a", { started: at(0), ended: at(READABLE_SPAN - 1) }), long = step("b", { started: at(0), ended: at(READABLE_SPAN) });
  assert.deepEqual([spanReads(stepSpan(short, scale, 0)), spanReads(stepSpan(long, scale, 0))], [false, true]);
  assert.equal(timed([short, short], scale, 0), false);
  assert.equal(timed([short, long], scale, 0), true);
  const waiting = step("w", { started: at(0), ended: undefined, state: "running", transcript: undefined });
  assert.equal(timed([waiting], scale, Date.parse(at(90))), false, "a worker that waits has run for no time");
  assert.equal(timed([{ ...waiting, transcript: "w.jsonl" }], scale, Date.parse(at(90))), true);
  assert.equal(timed([], scale, 0), false);
});

test("a step's body shows what it ran and left, with JSON read as fields", () => {
  const run = step("r", { kind: "run", name: "run: npm test", transcript: undefined });
  assert.deepEqual(stepBody(run, { detail: "npm test", result: JSON.stringify({ code: 0, stdout: "ok 12\n", stderr: "", durationMs: 40 }) }),
    [{ label: "Output", kind: "code", text: "ok 12" }]);
  const json = stepBody(run, { detail: "npm test -- --reporter json", result: JSON.stringify({ code: 0, stdout: `{"passed":3}`, stderr: "" }) });
  assert.deepEqual(json[0], { kind: "code", label: "Command", text: "npm test -- --reporter json" });
  assert.deepEqual(json[1], { label: "Output", kind: "fields", fields: [{ label: "Passed", kind: "text", text: "3" }] });
  const failed = step("f", { kind: "run", state: "failed", reason: "exit code 1: boom", transcript: undefined });
  assert.deepEqual(stepBody(failed, { detail: "x", result: JSON.stringify({ code: 1, stdout: "", stderr: "boom" }) }).map(part => part.kind), ["reason", "code"]);
  assert.deepEqual(stepBody(step("e", { kind: "run", transcript: undefined }), { result: JSON.stringify({ code: 3, stdout: "", stderr: "" }) }), [{ label: "Exit code", kind: "text", text: "3" }]);
  const merged = stepBody(run, { detail: "merge a", result: JSON.stringify({ branch: "v2/run", commit: "a".repeat(40), merged: ["a"], conflicts: [] }) });
  assert.deepEqual(merged[1], { kind: "fields", fields: [{ label: "Branch", kind: "branch", branch: "v2/run" }, { label: "Commit", kind: "commit", commit: "a".repeat(40) }, { label: "Merged", kind: "list", inline: true, items: [{ label: "", kind: "text", text: "a" }] }] });
  assert.deepEqual(stepBody(step("l", { kind: "log", transcript: undefined }), { detail: "two\nlines", result: "null" }), [{ kind: "text", text: "two\nlines" }]);
  assert.deepEqual(stepBody(step("l", { kind: "log", state: "failed", reason: "x: boom", transcript: undefined }), { detail: "x: boom" }), [{ kind: "reason", text: "x: boom" }]);
  const post = stepBody(step("p", { kind: "post", transcript: undefined }), { detail: "Done.\n\n```json\n{\"found\":2}\n```", result: `{"seq":9}` });
  assert.deepEqual(post, [{ kind: "markdown", text: "Done." }, { kind: "fields", fields: [{ label: "Found", kind: "text", text: "2" }] }]);
  assert.deepEqual(stepBody(step("a", { transcript: undefined, state: "failed", reason: "no place" }), { detail: "Read it" }), [{ kind: "reason", text: "no place" }, { kind: "text", text: "Read it", label: "Prompt" }]);
  assert.deepEqual(stepBody(step("l", { kind: "log", transcript: undefined })), []);
  assert.deepEqual(reading(`{"token": [masked]}`), { fields: [{ label: "Token", kind: "text", text: "[masked]" }] });
});

test("a result reads as text or as fields, never as JSON", () => {
  assert.deepEqual(reading("All done"), { text: "All done" });
  assert.deepEqual(reading("42"), { text: "42" });
  assert.deepEqual(reading("{not json"), { text: "{not json" });
  assert.deepEqual(reading(`{"found":2}`), { fields: [{ label: "Found", kind: "text", text: "2" }] });
  assert.deepEqual(reading(`{"token": [masked], "ok": true}`), { fields: [{ label: "Token", kind: "text", text: "[masked]" }, { label: "Ok", kind: "text", text: "Yes" }] });
});

test("scalars read as labelled rows with plain words", () => {
  const sha = "18b5fa2b8d9825dcdd238bbdcb671334c70eb35b";
  assert.deepEqual(jsonFields({ filesChanged: 1200, tests_passed: true, "dry-run": false, prUrl: "https://github.com/a/b/pull/1", durationMs: 90_000, nothing: null, blank: "", none: [], commit: "18b5fa2", head: sha, targetBranch: "v2/x", at: "2026-01-01T10:00:00Z" }).map(({ label, ...rest }) => [label, rest.kind, "text" in rest ? rest.kind === "text" && rest.text.length > 0 : true]), [
    ["Files changed", "text", true], ["Tests passed", "text", true], ["Dry run", "text", true], ["PR URL", "link", true], ["Duration", "text", true], ["Commit", "commit", true], ["Head", "commit", true], ["Target branch", "branch", true], ["At", "text", true],
  ]);
  assert.deepEqual(jsonFields({ durationMs: 90_000, count: 1200, ok: false }).map(item => item.kind === "text" && item.text), ["1m 30s", (1200).toLocaleString(), "No"]);
});

test("lists read as text, objects as groups, and a deep or long value says how much there is", () => {
  const value = {
    short: ["a", "b"],
    files: ["next/web/src/lib/workflow.ts", "next/web/src/lib/ui/RunSteps.svelte", "next/web/src/lib/ui/RunFields.svelte"],
    many: Array.from({ length: 25 }, (_, i) => `item ${i}`),
    people: Array.from({ length: 12 }, (_, i) => ({ name: `p${i}` })),
    reviews: [{ path: "a.go", score: 3 }, { score: 4 }],
    totals: { passed: 3, nested: { deeper: { deepest: { x: 1, y: 2 }, list: [{ a: 1 }] } } },
    report: "x".repeat(5000),
    quoted: `{"inner":[1,2]}`,
  };
  const [short, files, many, people, reviews, totals, report, quoted] = jsonFields(value as Json);
  assert.deepEqual(short, { label: "Short", kind: "list", inline: true, items: [{ label: "", kind: "text", text: "a" }, { label: "", kind: "text", text: "b" }] });
  assert.equal(files.kind === "list" && files.inline, false);
  assert.deepEqual([many.kind, many.kind === "text" && many.text], ["text", "25 items"]);
  assert.deepEqual([people.kind, people.kind === "text" && people.text], ["text", "12 items"]);
  assert.deepEqual(reviews, { label: "Reviews", kind: "group", fields: [{ label: "a.go", kind: "group", fields: [{ label: "Score", kind: "text", text: "3" }] }, { label: "2", kind: "group", fields: [{ label: "Score", kind: "text", text: "4" }] }] });
  assert.deepEqual(totals, { label: "Totals", kind: "group", fields: [{ label: "Passed", kind: "text", text: "3" }, { label: "Nested", kind: "group", fields: [{ label: "Deeper", kind: "group", fields: [{ label: "Deepest", kind: "text", text: "2 fields" }, { label: "List", kind: "text", text: "1 item" }] }] }] });
  assert.deepEqual(report, { label: "Report", kind: "text", text: `${(5000).toLocaleString()} characters` });
  assert.equal(quoted.kind, "group");
  assert.deepEqual(jsonFields([{ path: "a" }, { path: "b" }]), [{ label: "Items", kind: "group", fields: [{ label: "", kind: "text", text: "a" }, { label: "", kind: "text", text: "b" }] }]);
});

test("a result leaves out what the page already shows, whatever the script called it", () => {
  const sha = "18b5fa2b8d9825dcdd238bbdcb671334c70eb35b";
  const steps = [step("fix-1"), step("fix-2", { state: "failed" }), step("key-3", { name: "Read the logs" })];
  const merge = { branch: "v2/s-run", commit: sha, merged: ["fix-1"], conflicts: [{ step: "fix-2", branch: "v2/s-2", files: ["f"] }] };
  const shown = shownFacts({ branch: "v2/ses_1", name: "sweep", repo: "a/b", commit: "abc1234" }, steps, merge);
  assert.deepEqual(jsonFields({ merged: ["fix-1"], steps: [{ step: "fix-1", text: "x", files: ["a"] }, { name: "Read the logs" }], outcome: { branch: "v2/s-run", commit: sha, merged: ["fix-1"], conflicts: [] }, branch: "v2/s-run", head: sha, repo: "a/b", count: 3, summary: "Fixed one" }, shown).map(item => item.label), ["Count", "Summary"]);
  assert.deepEqual(jsonFields({ stepsDone: 3, steps: 3, failed: 1, merged: 1, conflicts: 1 }, shown).map(item => item.label), ["Steps done"]);
  assert.deepEqual(jsonFields({ others: ["fix-1", "something else"] }, shown).map(item => item.label), ["Others"]);
  assert.deepEqual(reading(JSON.stringify({ branch: "v2/s-run", commit: sha, merged: ["fix-1"], conflicts: [{ step: "fix-2", branch: "v2/s-2", files: ["f"] }] }), shown), { fields: [] });
  assert.deepEqual(reading(`["fix-1","fix-2"]`, shown), { fields: [] });
});

test("a value is left out for repeating the page, never for its key's name", () => {
  const steps = [step("a1b2c3d4e5f6a7b8c9d0"), step("second", { state: "failed" })];
  const merge = { branch: "v2/ses_1-run", commit: "abc", merged: ["a"], conflicts: [] };
  const shown = shownFacts({ branch: "v2/ses_1", name: "sweep" }, steps, merge);
  const unrelated = { steps: ["Wash", "Dry", "Fold"], merged: ["pricing.md and docs.md"], conflicts: ["a Tuesday booking"], failed: ["/pricing", "/careers"] };
  assert.deepEqual(jsonFields(unrelated, shown).map(item => item.label), ["Steps", "Merged", "Conflicts", "Failed"]);
  assert.deepEqual(jsonFields({ steps: [{ branch: "v2/ses_1-a1b2c3d4e5f6", commit: "fed", files: ["a"] }, { name: "second" }] }, shown), [], "a step's own worktree branch names it");
  assert.deepEqual(jsonFields({ steps: [{ branch: "v2/other-a1b2c3d4e5f6", files: ["a"] }] }, shown).map(item => item.label), ["Steps"]);
  assert.deepEqual(jsonFields({ steps: 2, failed: 1, conflicts: 0, merged: 1 }, shown), [], "a section's own count");
  assert.deepEqual(jsonFields({ steps: 7, failed: 5 }, shown).map(item => item.label), ["Steps", "Failed"], "another number says something else");
});

test("Runs lists the running runs first, newest first, then the rest, the latest to end first", () => {
  const run = (session: string, fields: Partial<WorkflowInfo> = {}): WorkflowInfo => status({ session, state: "done", ...fields }).run;
  const runs = [run("old", { ended: "2026-01-01T01:00:00Z" }), run("live", { state: "running" }), run("closing", { ended: "2026-01-03T00:00:00Z" }), run("new", { state: "failed", ended: "2026-01-02T00:00:00Z" }),
    run("unended", { started: "2026-01-01T12:00:00Z", state: "stopped" }), run("later", { state: "running", started: "2026-01-02T00:00:00Z" })];
  const { running, past } = roomRuns(runs);
  assert.deepEqual(running.map(item => item.session), ["later", "live"]);
  assert.deepEqual(past.map(item => item.session), ["closing", "new", "unended", "old"]);
  assert.deepEqual(runCounts({ steps: 1, failed: 0 }), ["1 step"]);
  assert.deepEqual(runCounts({ steps: 60, failed: 4 }), ["60 steps", "4 failed"]);
  assert.deepEqual(runCounts({ failed: 0 }), []);
});

test("the panel's run rows: running runs, then a count of the past ones that lists them when open", () => {
  const run = (session: string, fields: Partial<WorkflowInfo> = {}): WorkflowInfo => status({ session, state: "done", ...fields }).run;
  const runs = [run("old", { ended: "2026-01-01T01:00:00Z" }), run("live", { state: "running" }), run("new", { state: "failed", ended: "2026-01-02T00:00:00Z" })];
  const names = (open: boolean) => runRows(runs, open).map(row => row.kind === "run" ? row.run.session : row.kind === "head" ? "head" : `${row.kind} ${row.count} ${row.failed}f${row.open ? " open" : ""}`);
  assert.deepEqual(names(false), ["head", "live", "past 2 1f"]);
  assert.deepEqual(names(true), ["head", "live", "past 2 1f open", "new", "old"]);
  assert.deepEqual(runRows(runs, false, "old").map(row => row.kind === "run" ? row.run.session : row.kind), ["head", "live", "past", "old"], "the open page's run stays under a closed fold");
  assert.deepEqual(runRows([], true), []);
  assert.deepEqual(runRows([runs[1]], false).map(row => row.kind), ["head", "run"]);
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

test("a run that ended leaves one row in the room feed and none in a DM, between the work around it", () => {
  const ended = { seq: 3, id: "e", at: "2026-01-01T00:00:03Z", from: { kind: "engine" }, type: "workflow.ended", body: { run: "wf", session: "crew", name: "sweep", state: "failed", steps: 12, failed: 2, usage } };
  const decoded = decodeEvent(ended) as Event;
  assert.ok(decoded);
  const rows = feedRows([decoded], { kind: "feed" }).filter(row => row.kind !== "day");
  assert.deepEqual(rows.map(row => row.kind === "run" && [row.key, row.run.name, row.run.state, row.run.steps]), [["run3", "sweep", "failed", 12]]);
  assert.deepEqual(feedRows([decoded], { kind: "dm", session: "crew" }), []);
  const old = decodeEvent({ ...ended, body: { ...ended.body, steps: undefined } });
  assert.ok(old, "an event from before steps were counted still decodes");
  const late = { ...decoded, seq: 4, at: "2026-01-02T00:00:03Z" } as Event;
  assert.equal(feedRows([decoded, late], { kind: "feed" }).filter(row => row.kind === "day").length, 2);
});

test("workflow text is never heuristically hidden or retyped, even if it looks like JSON", () => {
  const base = { from: { kind: "session" as const, session: "crew" }, at: "2026-01-01T00:00:03Z" };
  const reply = { reply: { kind: "final" as const, target: { kind: "owner" as const } } };
  const turn = (seq: number, session: string, final: string, posted?: string): Event => ({ seq, id: `t${seq}`, ...base, type: "turn.ended", body: { session, turn: "t", outcome: "completed", final, usage, replies: [{ ...reply, posted }] } });
  const ended = (seq: number): Event => ({ seq, id: `e${seq}`, ...base, type: "workflow.ended", body: { run: "wf", session: "crew", name: "sweep", state: "done", steps: 3, failed: 0, usage } });
  const posts = (events: Event[]) => feedRows(events, { kind: "feed" }, [], 0, new Set()).flatMap(row => row.kind === "post" ? [row.text] : row.kind === "run" ? ["run row"] : []);
  const json = `{"merged":{"branch":"v2/x"},"steps":[]}`;
  assert.deepEqual(posts([turn(1, "crew", json), ended(2)]), [json, "run row"]);
  assert.deepEqual(posts([turn(1, "crew", `["a","b"]`), ended(2)]), [`["a","b"]`, "run row"]);
  assert.deepEqual(posts([turn(1, "crew", "All done"), ended(2)]), ["All done", "run row"]);
  assert.deepEqual(posts([turn(1, "crew", "42"), ended(2)]), ["42", "run row"]);
  assert.deepEqual(posts([turn(1, "root", json)]), [json], "only a crew's");
  assert.deepEqual(posts([turn(1, "crew", json)]), [json], "no inference from an absent run event");
  const post: Event = { seq: 1, id: "p1", ...base, type: "post.created", body: { text: json, home: { kind: "feed" } } };
  assert.deepEqual(posts([post, turn(2, "crew", json, "p1"), ended(3)]), [json, "run row"], "a post that carried the reply");
  assert.deepEqual(posts([post, ended(3)]), [json, "run row"], "a post of its own is the script's to make");
});

test("a step change decodes with seq 0 and adds no row to any conversation", () => {
  const change = { seq: 0, id: "e", at: "2026-01-01T00:00:00Z", from: { kind: "engine" }, type: "workflow.step_changed", body: { run: "wf", session: "crew", step: { ...step("a"), lane: "g1" }, steps: 1, running: 1, failed: 0 } };
  const decoded = decodeEvent(change);
  assert.ok(decoded);
  assert.equal(decodeEvent({ ...change, body: { run: "wf", session: "crew", step: { key: "a" } } }), undefined);
  assert.deepEqual(feedRows([{ ...(decoded as Event), seq: 1 }], { kind: "feed" }).filter(row => row.kind !== "day"), []);
  const line = { ...change, type: "workflow.status_changed", body: { run: "wf", session: "crew", status: "Wave 3: 6 of 7 landed" } };
  const said = decodeEvent(line);
  assert.ok(said);
  assert.equal(decodeEvent({ ...line, body: { run: "wf", session: "crew" } }), undefined);
  assert.deepEqual(feedRows([{ ...(said as Event), seq: 1 }], { kind: "feed" }).filter(row => row.kind !== "day"), []);
});

// A step that ran from one minute to another of the run.
const at = (minute: number) => new Date(Date.UTC(2026, 0, 1) + minute * 60_000).toISOString();
const ran = (key: string, from: number, to: number | undefined, fields: Partial<WorkflowStep> = {}): WorkflowStep => step(key, { started: at(from), ended: to === undefined ? undefined : at(to), ...to === undefined && { state: "running" }, ...fields });

test("the flowchart counts each lane's moves between stages, loops and skips included, and a failed node joins only on a failure", () => {
  const steps = [
    ran("c1", 0, 1, { phase: "contract", lane: "g1" }), ran("c2", 0, 1, { phase: "contract", lane: "g2" }),
    ran("w1", 1, 3, { phase: "write", lane: "g1" }), ran("w2", 1, 2, { phase: "write", lane: "g2" }), ran("w2b", 2, 3, { phase: "write", lane: "g2" }),
    ran("r1", 3, 5, { phase: "review", lane: "g1" }), ran("l2", 3, 4, { phase: "land", lane: "g2", kind: "run" }),
    ran("f1", 5, 6, { phase: "fix", lane: "g1" }), ran("r1b", 6, 7, { phase: "review", lane: "g1", state: "failed" }), ran("r1c", 7, undefined, { phase: "review", lane: "g1" }),
  ];
  const graph = flow(steps);
  assert.deepEqual(graph.nodes.map(node => [node.title, node.done, node.running, node.failed, node.lanes]),
    [["contract", 2, 0, 0, 2], ["write", 3, 0, 0, 2], ["review", 1, 1, 1, 1], ["land", 1, 0, 0, 1], ["fix", 1, 0, 0, 1]]);
  assert.deepEqual(graph.nodes[2].live.map(item => item.key), ["r1c"]);
  assert.deepEqual(graph.edges.map(edge => `${edge.from}>${edge.to}:${edge.count}`).sort(),
    ["contract>write:2", "fix>review:1", "review>fix:1", "review>\u0000failed:1", "write>land:1", "write>review:1"].sort());
  assert.deepEqual(graph.failed.map(item => item.key), ["r1b"]);
  assert.equal(graph.laned, true);
  assert.equal(stepTitle(steps[0]), "g1");
  assert.equal(flow(steps.filter(item => item.state !== "failed")).edges.some(edge => edge.to === FAILED), false);
});

test("without lanes the stages join in the order they first ran, and a stage no lane reached joins its neighbours", () => {
  const plain = flow([ran("a", 0, 1, { phase: "read" }), ran("b", 1, 2, { phase: "build" }), ran("c", 2, 3, { phase: "read" }), ran("d", 3, 4, { phase: "ship" })]);
  assert.deepEqual(plain.edges.map(edge => `${edge.from}>${edge.to}:${edge.count}`), ["read>build:0", "build>ship:0"]);
  assert.equal(plain.laned, false);
  const mixed = flow([ran("s", 0, 1, { phase: "setup", kind: "run" }), ran("a", 1, 2, { phase: "write", lane: "x" }), ran("b", 2, 3, { phase: "review", lane: "x" })]);
  assert.deepEqual(mixed.edges.map(edge => `${edge.from}>${edge.to}:${edge.count}`), ["write>review:1", "setup>write:0"]);
  // One run with no phases at all is one node of every step.
  assert.deepEqual(flow([ran("a", 0, 1), ran("b", 1, 2)]).nodes.map(node => [node.title, node.steps.length]), [["Steps", 2]]);
});

test("a child takes its workflow step's stage, and stages take their colours in the order they first ran", () => {
  const steps = [ran("wf", 0, 3, { kind: "workflow", phase: "build" }), ran("a", 1, 2, { parent: "wf" }), ran("b", 0, 1, { phase: "read" }), ran("c", 2, 3)];
  assert.equal(stageOf(steps).get("a"), "build");
  assert.deepEqual(stagesOf(steps).map(stage => [stage.title, stage.colour]), [["build", "var(--stage-1)"], ["read", "var(--stage-2)"], ["Other steps", "var(--muted)"]]);
});

test("the timeline packs workers into slots, folds the steps a resume kept to the left, and draws commands on a row of their own", () => {
  const info = { started: at(30), state: "running" as const };
  const steps = [
    ran("old1", 0, 10, { phase: "write" }), ran("old2", 5, 20, { phase: "write" }),
    ran("a", 30, 40, { phase: "review", lane: "g1" }), ran("b", 32, 36, { phase: "review" }), ran("c", 41, 45, { phase: "fix" }), ran("d", 37, undefined, { phase: "review" }),
    ran("land", 36, 37, { kind: "run", phase: "land" }), ran("note", 38, 38, { kind: "log" }), ran("wait", 44, undefined, { transcript: undefined }),
  ];
  const chart = timeline(info, steps, Date.parse(at(50)));
  const rows = Object.fromEntries(chart.bars.map(bar => [bar.step.key, bar.row]));
  assert.deepEqual(rows, { old1: 0, old2: 1, a: 0, b: 1, d: 1, c: 0 });
  assert.equal(chart.rows, 2);
  assert.equal(chart.fold, 16);
  const bar = (key: string) => chart.bars.find(item => item.step.key === key)!;
  assert.equal(bar("old1").left, 0);
  assert.ok(bar("old2").left + bar("old2").width <= 16.0001 && bar("old1").kept && !bar("a").kept);
  assert.equal(bar("a").left, 16);
  assert.equal(chart.now, 100);
  assert.deepEqual(chart.commands.map(item => item.step.key), ["land"]);
  assert.equal(chart.resumed, "Resumed at 30m, 2 earlier steps kept");
  assert.equal(chart.ticks[0].label, "0m");
  assert.deepEqual(chart.ticks.map(tick => tick.label), ["0m", "30m", "35m", "40m", "45m"]);
  assert.ok(chart.ticks.every((tick, index) => !index || tick.at >= 16));
  // A run without workers packs whatever ran, and an ended one has no now line.
  const commands = timeline({ started: at(0), ended: at(4), state: "done" }, [ran("x", 0, 2, { kind: "run" }), ran("y", 1, 3, { kind: "run" })], Date.parse(at(9)));
  assert.deepEqual([commands.rows, commands.commands.length, commands.fold, commands.now, commands.resumed], [2, 0, 0, undefined, ""]);
});

test("the figures split worker time by stage, name the longest step and count what ran before a resume", () => {
  const steps = [ran("old", 0, 10, { phase: "write" }), ran("a", 30, 45, { phase: "review", profile: "opus" }), ran("b", 30, 35, { phase: "write" }), ran("land", 35, 36, { kind: "run" })];
  const spent = figures({ started: at(30), ended: at(50) }, steps, Date.parse(at(60)));
  assert.equal(spent.work, 30 * 60_000);
  assert.deepEqual(spent.split.map(part => [part.title, part.ms / 60_000]), [["write", 15], ["review", 15]]);
  assert.equal(spent.longest?.step.key, "a");
  assert.deepEqual([spent.wall / 60_000, spent.earlier / 60_000], [20, 10]);
  assert.deepEqual([0, 1, 29_999, 30_000, 59_999, 60_000].map(minutesLabel), ["<1m", "<1m", "<1m", "<1m", "<1m", "1m"]);
  assert.deepEqual([minutesLabel(28 * 60_000), minutesLabel(60 * 60_000), minutesLabel(125 * 60_000)], ["28m", "1h", "2h 5m"]);
});
