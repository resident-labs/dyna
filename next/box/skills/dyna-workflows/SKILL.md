---
name: dyna-workflows
description: Use when you write, run, resume or reload a Dyna workflow script, or a task needs many agents coordinated in code.
---

# Workflow authoring reference

A workflow structures work across many agents — to be comprehensive (decompose and cover in parallel), to be confident (independent perspectives and adversarial checks before committing), or to take on scale one context can't hold (migrations, audits, broad sweeps). The script is where you encode that structure: what fans out, what verifies, what synthesizes. Fan out wide: a sweep over every file, every issue or every claim is hundreds of agents, one per item, and the script handles that as easily as five.

The right move is often **hybrid**: scout inline first (list the files, find the channels, scope the diff) to discover the work-list, then run a workflow that pipelines over it. You don't need to know the shape before the *task* — only before the *orchestration step*.

Common single-phase workflows you can chain across turns:
- **Understand** — parallel readers over relevant subsystems → structured map
- **Design** — judge panel of N independent approaches → scored synthesis
- **Review** — dimensions → find → adversarially verify (the review-changes example in the workflow tool's description)
- **Research** — multi-modal sweep → deep-read → synthesize
- **Migrate** — discover sites → transform each (worktree) → verify → merge

For larger work, run several in sequence — read each result before deciding the next phase. You stay in the loop; each workflow is one well-scoped fan-out. A workflow is one box with many workers; a child session is for one stream of work that owns a branch or keeps its context across wakes.

Pass the script inline, as `script`, or as a file to `dyna workflow run FILE`; each run keeps its script, and `workflow reload` and `--resume` take a file again (see Resume).

Every script must begin with `export const meta = {...}`:

```js
export const meta = {
  name: 'find-flaky-tests',
  description: 'Find flaky tests and propose fixes',    // one line
  profiles: ['<profile>'],                              // from dyna_profiles; the first is the default
}
// script body starts here — use agent()/run()/parallel()/pipeline()/merge()/phase()/status()/log()
phase('Scan')
const flaky = await agent('grep CI logs for retry markers', { schema: FLAKY_SCHEMA })
```

The `meta` object must be a PURE LITERAL — no variables, function calls, spreads, or template interpolation. Only comments may come before it. Required fields: `name` (letters, digits, `.`, `_` or `-`, at most 57; the run's session is `<name>-<6 characters>`) and `profiles`. Optional: `description`, `size` (`agent`, `small`, `medium`, `large` or `xlarge`; table in dyna-images; else the room's `size` setting), `repo` (the room's default repository unless given; a run with none has no `worktree` or `merge()`), `gpu` (GiB of GPU memory for the run's box; a worker has none), `host` (pin the run's box to a named host, subject to the room host rule; `host` or `sameHost` on `workflow run` wins over `meta.host`, and `resume` keeps the previous run's host unless a new one is given). An unknown key refuses the start, and so does an unknown profile. A refused start names the setting; `dyna_workflows` lists what is off, root changes it with `dyna workflow set` (dyna-admin), others ask their parent.

Script body hooks:
- agent(prompt: string, opts?: {label?: string, phase?: string, lane?: string, schema?: object, profile?: string, worktree?: boolean, timeout?: number}): Promise<any> — spawn a worker. Without schema, returns its final text as a string. With schema (a JSON Schema), returns the worker's validated `StructuredOutput` result. If the worker doesn't use the tool, Dyna checks the turn's closing messages, newest first, for a whole JSON value, a last fenced block, or a last JSON object or array. If none matches, the worker has up to three turns to return a valid result. opts.label overrides the display label; give every step a distinct one, since `failedSteps`, `merged`, `conflicts` and `.step` name steps by label. opts.phase explicitly assigns this agent to a progress group (use this inside pipeline()/parallel() stages to avoid races on the global phase() state — same phase string → same group). opts.lane names the subject the step is about (`lane: g.id`); the run page draws each lane's path through the phases. opts.profile picks the model for this agent call: omit it to use the script's first profile; set it only when you're highly confident a different one fits; when unsure, omit. Use a cheaper profile from meta.profiles for mechanical stages and a stronger one for the hardest verify/judge stages — dyna_profiles rates each. opts.timeout is in seconds (default 5 hours unless the room sets it). opts.worktree: true runs the worker in its own git worktree and branch — use ONLY when workers write files in parallel and would otherwise conflict; its work, except ignored files, is committed. Dyna pushes the branch only if the final commit differs from the pinned base. The tree is removed either way, and agent() returns `{step, text, branch, commit, files}` (`output` in place of `text` with a schema), `files` being `[]` for no change. A result with `unpublished: true` ended at the pinned base and has no remote step branch; use its `commit`. Workers do not start workers. A failed call throws an `Error` with `.step` and `.reason`: an unlisted profile, a timeout, no final text, no matching result in three turns, a failed worktree push. If a failed worker produced text, `.rawText` holds its last assistant text (up to 1 MiB), separate from `.reason`.
- run(command: string | string[], opts?: {cwd?, env?, timeout?, check?, label?, phase?, lane?}): Promise<{code, stdout, stderr, durationMs}> — run a command in the box with no model: a string runs in `sh -c`, a list directly. It takes the build lock, `cwd` is relative to the main tree unless absolute, and stdout and stderr are the last 64 KiB each. `check` defaults to `true`: a nonzero exit throws `exit code N: <stderr>`; with `check: false` read `code`. Use it for the deterministic steps (list the files, run the tests) so no agent pays for them.
- pipeline(items, stage1, stage2, ...): Promise<any[]> — run each item through all stages independently, NO barrier between stages. Item A can be in stage 3 while item B is still in stage 1. This is the DEFAULT for multi-stage work. Wall-clock = slowest single-item chain, not sum-of-slowest-per-stage. Every stage callback receives (prevResult, originalItem, index) — use originalItem/index in later stages to label work without threading context through stage 1's return value. A stage that throws drops that item to `{failed: true, step, reason, rawText?}` and skips its remaining stages.
- parallel(thunks: Array<() => Promise<any>>): Promise<any[]> — run tasks concurrently. This is a BARRIER: awaits all thunks before returning. A thunk that throws (or whose agent errors) resolves to `{failed: true, step, reason, rawText?}` in the result array — the call itself does not throw, so filter with `!r.failed` before using the results. Use ONLY when you genuinely need all results together.
- merge(results, opts?: {base?, label?}): Promise<{branch, commit, merged, conflicts}> — merge the worktree results (each `commit`, in order) from `base` (the pinned commit by default) into `<run session branch>-run`. A conflict or failed `parallel` entry lands in `conflicts`; misuse or git failure throws. See Worktrees.
- workflow(path: string, args?: any): Promise<any> — run another script inline as a sub-step and return what it returns. Nests one level; the child's `meta` is ignored, and it reruns on every call.
- post(to, text, data?, opts?: {lane?}): Promise<{event, seq}> — post to the run's parent (`to` is `parent` or its name; any other mention fails the step), once however often the script replays.
- serve(handler: async (event, key, state) => void): void — only in a script attached to a subscription (events.md): the body is setup that runs at each box start, and once it ends the run stays up and calls `handler` for each event, with the event envelope, its key and that key's `state` (`get()`, `set(v)`). Keys run concurrently in one box; a key's events run in order. Call it once, in the script itself. A subscription's script whose body ends without it is refused; `workflow run` refuses it.
- status(text: string): void — set the run's one-line status under its name on the run page and in the Runs list. It is not a step and does not replay.
- log(message: string): void — emit a progress message into the run's journal. phase(title: string): void — start a new phase; subsequent agent() calls are grouped under this title.
- args: any — the JSON text passed as `--args` or the tool's `args`, already parsed (`null` without it): `JSON.parse(args)` throws on an object. Use it to parameterize a script — a research question, target path or config object — instead of a side-channel file.

Workers get the shared base prompt, the AGENTS.md files and their tools; not the session's own prompt or the skills index. A result too large to type out (a long list, a document) belongs in a file in the worker's tree: tell it to write the file and return the path, and read the file with `run()`. A worker whose reply hits the output limit is told so.

Workers can reach MCP and GitHub tools through tool_search — schemas load on demand per worker. There is no `gh` in a box.

Workers get the same AGENTS.md files that you did — don't tell them to re-read those or paste their rules into the prompt; name the specific rule a stage needs, if any. They do not get dyna_* sections, `args` or earlier steps, so pass paths, branches, commits and results in the prompt.

Scripts are plain JavaScript, NOT TypeScript — type annotations (`: string[]`), interfaces, and generics fail to parse. The script body runs in an async context — use await directly. Standard JS built-ins (JSON, Math, Array, etc.) are available — EXCEPT `Date.now()`/`Math.random()`/argless `new Date()`, which throw (they would break resume); pass timestamps in via `args`, stamp results after the workflow returns, and for randomness vary the agent prompt/label by index. The VM has no timers, files or network. A step not awaited is cancelled when the script returns, and fails.

DEFAULT TO pipeline(). Only reach for a barrier (parallel between stages) when you genuinely need ALL prior-stage results together.

A barrier is correct ONLY when stage N needs cross-item context from all of stage N-1:
- Dedup/merge across the full result set before expensive downstream work
- Early-exit if the total count is zero ("0 bugs found → skip verification entirely")
- Stage N's prompt references "the other findings" for comparison

A barrier is NOT justified by:
- "I need to flatten/map/filter first" — do it inside a pipeline stage: pipeline(items, stageA, r => transform([r]).flat(), stageB)
- "The stages are conceptually separate" — that's what pipeline() models. Separate stages ≠ synchronized stages.
- "It's cleaner code" — barrier latency is real. If 5 finders run and the slowest takes 3× the fastest, a barrier wastes 2/3 of the fast finders' idle time.

Smell test: if you wrote
  const a = await parallel(...)
  const b = transform(a)        // flatten, map, filter — no cross-item dependency
  const c = await parallel(b.map(...))
that middle transform doesn't need the barrier. Rewrite as a pipeline with the transform inside a stage. When in doubt: pipeline.

## Concurrency

Pass hundreds of items to pipeline() or parallel(): every call is accepted, the box runs only so many workers at once and queues the rest as slots free up, and all of them complete. There is no cap on the items of one call or on a run's total agents. The limits on how many run at once:
- the box: one worker per 512 MiB of its memory, 4 to 64 by `size` (the run's box is one box, so a larger `size` in `meta` is how a wide fan-out gets faster);
- each profile's `maxConcurrent` in dyna_profiles, where it has one;
- the room's `maxWorkers`, which can cap a run lower, read when its box starts (root changes it, others ask their parent).

A worker waits for a box slot, then its profile's slot; its `timeout` starts once it holds both, and waiting on a profile holds a box slot. One build runs at a time: `run()` and the workers' `cargo`, `go`, `npm`, `make` and similar share one lock, and time queued for it does not count against a timeout.

When a barrier IS correct — dedup across all findings before expensive verification:

```js
const all = await parallel(DIMENSIONS.map(d => () => agent(d.prompt, {schema: FINDINGS_SCHEMA})))
const deduped = dedupeByFileAndLine(all.filter(r => !r.failed).flatMap(r => r.findings))  // <-- genuinely needs ALL at once
const verified = await parallel(deduped.map(f => () => agent(verifyPrompt(f), {schema: VERDICT_SCHEMA})))
```

Loop-until-count pattern — accumulate to a target:

```js
const bugs = []
while (bugs.length < 10) {
  const result = await agent("Find bugs in this codebase.", {schema: BUGS_SCHEMA})
  bugs.push(...result.bugs)
  log(`${bugs.length}/10 found`)
}
```

Composing patterns — exhaustive review (find → dedup vs seen → diverse-lens panel → loop-until-dry):

```js
const seen = new Set(), confirmed = []
let dry = 0
while (dry < 2) {                                              // loop-until-dry
  const found = (await parallel(FINDERS.map(f => () =>          // barrier: collect all finders this round
    agent(f.prompt, {phase: 'Find', schema: BUGS})))).filter(r => !r.failed).flatMap(r => r.bugs)
  const fresh = found.filter(b => !seen.has(key(b)))           // dedup vs ALL seen — plain code, not an agent
  if (!fresh.length) { dry++; continue }
  dry = 0; fresh.forEach(b => seen.add(key(b)))
  const judged = await parallel(fresh.map(b => () =>           // every fresh bug judged concurrently...
    parallel(['correctness','security','repro'].map(lens => () =>   // ...each by 3 distinct lenses
      agent(`Judge "${b.desc}" via the ${lens} lens — real?`, {phase: 'Verify', schema: VERDICT})))
      .then(vs => ({ b, real: vs.filter(v => !v.failed).filter(v => v.real).length >= 2 }))))
  confirmed.push(...judged.filter(v => v.real).map(v => v.b))
}
return confirmed
// dedup vs `seen`, NOT `confirmed` — else judge-rejected findings reappear every round and it does not converge.
```

Quality patterns — common shapes; pick by task and compose freely:
- Adversarial verify: spawn N independent skeptics per finding, each prompted to REFUTE. Kill if ≥majority refute. Prevents plausible-but-wrong findings from surviving.
  ```js
  const votes = await parallel(Array.from({length: 3}, () => () =>
    agent(`Try to refute: ${claim}. Default to refuted=true if uncertain.`, {schema: VERDICT})))
  const survives = votes.filter(v => !v.failed).filter(v => !v.refuted).length >= 2
  ```
- Perspective-diverse verify: when a finding can fail in more than one way, give each verifier a distinct lens (correctness, security, perf, does-it-reproduce) instead of N identical refuters — diversity catches failure modes redundancy can't.
- Judge panel: generate N independent attempts from different angles (e.g. MVP-first, risk-first, user-first), score with parallel judges, synthesize from the winner while grafting the best ideas from runners-up. Beats one-attempt-iterated when the solution space is wide.
- Loop-until-dry: for unknown-size discovery (bugs, issues, edge cases), keep spawning finders until K consecutive rounds return nothing new. Simple counters (while count < N) miss the tail.
- Multi-modal sweep: parallel agents each searching a different way (by-container, by-content, by-entity, by-time). Each is blind to what the others surface; useful when one search angle won't find everything.
- Completeness critic: a final agent that asks "what's missing — modality not run, claim unverified, source unread?" What it finds becomes the next round of work.
- Write, review and fix: a writer in a worktree, a reviewer whose prompt names the commit, and a fixer, for a fixed number of rounds.
- No silent caps: if a workflow bounds coverage (top-N, no-retry, sampling), `log()` what was dropped — silent truncation reads as "covered everything" when it didn't.

Scale to what the task asks for. "find any bugs" → a few finders, single-vote verify. "thoroughly audit this" or "be comprehensive" → one finder per file or per item, a 3–5 vote adversarial pass, a synthesis stage. When unsure, lean toward thoroughness for research/review/audit requests and toward brevity for quick checks.

These patterns aren't exhaustive — compose novel harnesses when the task calls for it (tournament brackets, self-repair loops, staged escalation, whatever fits).

Use a workflow for multi-step orchestration where control flow should be deterministic (loops, conditionals, fan-out) rather than model-driven. A worker that skips everything still reports done, and a `done` run is not proof of finished work: check `files.length`, a schema field, `conflicts`, `merged` and the failed steps. Don't prohibit the tools a worker needs to build and verify its change. Give writers separate files.

## Worktrees and merge

- `worktree: true` gives a worker its own tree and branch `<run session branch>-<12 hex>` from the pinned commit (`commit` in `workflow list`). Without it, workers share the main tree, whose uncommitted files are lost on replacement or resume.
- `merge(results)` takes the worktree results in order. A later merge replaces the run branch; `{base: earlier.commit}` builds on an earlier one. It reads `commit`, not the tip of `branch`: after a push, pass `{...result, commit: pushed}`. There is no rebase or repair: hand a conflict to an `agent()` in a worktree whose prompt names both branches.
- The main tree stays pinned. Test the merge in a temporary worktree, in one `run()` a replay cannot split; `test.code` is 0 when the merge passes:

```js
const dir = `/tmp/check-${merged.commit}`
const test = await run(`git fetch origin ${merged.branch} && git worktree add --detach ${dir} ${merged.commit} &&
  (cd ${dir} && go test ./...); s=$?; git worktree remove --force ${dir}; exit $s`, { check: false, label: 'test merge' })
```

## How a run ends

An uncaught throw ends the run `failed`; a return ends it `done`, however many steps failed. A return value or step result over 1 MiB as JSON fails it.
- A returned string is the starter's reply, plus `Failed steps (N): <names>` when any failed.
- Other values are reply data with `Workflow <name> finished.`: compact UTF-8 JSON. Up to 1024 bytes appears in the input; above, `data.json` is attached at `/box/attachments/<seq>/data.json`. `post()` data arrives the same way. Nothing is truncated.
- A throw gives `workflow <name>: <reason>`, a stop `Workflow <name> stopped.`.
- `dyna workflow status RUN` shows result, step states, failure reason and script version, and the newest merge; `dyna workflow list` gives run IDs, sessions and pinned commits. Return the worker output the caller needs.

## Steer, pause, stop

- A post mentioning the run's session reaches every worker running then, before its next model request. The script reads nothing; with no worker running the post stays undelivered.
- One worker: `dyna workflow steer RUN STEP TEXT`, or the workflow tool's steer with run, step and text. STEP is a label or unique key prefix; status lists steps. Steering changes neither its key nor replay.
- `dyna workflow pause RUN [STEP]` holds the run, or one worker, before its next model request; `dyna workflow unpause RUN [STEP]` lets it go on; without STEP it clears all holds. A run-wide pause also holds new agent() and run() calls. Steering waits for release. Pause survives restarts. RUN is a run ID or session name; the starter, its ancestors and the owner control the run.
- Stop: session stop on the run's session, or `dyna session stop SESSION`; status then says `stopped`, and running steps fail with `the run ended first`.

## Resume and reload

Every call is a keyed step. A key hashes kind, prompt or command, options other than `lane`, effective profile and enclosing `workflow()` step. A finished key returns its stored result: the same script and args give a full cache hit, changed prompts or options rerun that step, changing `meta.profiles[0]` reruns default-profile steps, and two identical calls are two steps. Name the commit in a reviewer's prompt so its verdict matches the code. Before diagnosing why a finished run returned an empty or unexpected result, read `dyna workflow status RUN`: its step journal records each worker's actual return value.

- `dyna workflow reload RUN FILE` (reload is for a running run) fixes a prompt or adds steps while finished work stays. A running step the new script calls keeps its worker: no second one starts; one it drops runs on until the run ends.
- `dyna workflow run FILE --resume RUN` (resume is for an ended run of the same room and repository) is a new session with the same pinned commit. Failed, cut-off and `workflow()` steps rerun; main-tree files are lost. It can push through any chain of resumes. Pushed worktree branches are fetched back on resume, but a step fails if its branch was deleted 14 days after the last run that used it ended.
- Editing a profile changes the model and cap for workers that start afterward, including in a running service. It does not reload the script or switch a running worker's model. If a declared profile is removed, new calls to it fail; a removed default does not fall back to another profile.

## Start a script from events

Root can attach a script to room events as a service: one run that stays up in one box, handles each event with `serve()` and keeps saved state per key. Read [events.md](events.md).
