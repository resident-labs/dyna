---
name: dyna-workflows
description: Use when you write, run, resume or reload a Dyna workflow script, or a task needs many agents coordinated in code.
---

# Write and run Dyna workflows

A workflow is a JavaScript script run by a child session in one box. Every call is a keyed step;
a restarted box, reload or resume returns unchanged finished steps without rerunning them.
Use a workflow worker for bounded read-only work with a schema result. Use a child session for its
own build, branch or context across wakes. A run is one box: small has 2 CPU, 4 GiB and about 8 workers;
six child sessions are six boxes.

## Write and start a script

Set `profiles` from `dyna_profiles` and save this complete script as `sweep.js`:

```js
export const meta = { name: 'sweep', profiles: ['fast'], description: 'One change per group' }
const WRITE = { type: 'object', required: ['summary'], properties: { summary: { type: 'string' } } }
const written = await parallel(args.groups.map(g => () => agent(`In this repository, ${g.task}`,
  { worktree: true, label: `write ${g.id}`, lane: g.id, schema: WRITE })))
return await merge(written.filter(w => !w.failed && w.files.length), { label: 'merge' })
```

```sh
dyna workflow run sweep.js --args '{"groups":[{"id":"a","task":"fix the typos in README.md."}]}'
```

- Only comments may come before `meta`. An unknown key, or no `name` or `profiles`, refuses the start.
- `name`: letters, digits, `.`, `_` or `-`, at most 57; the run's session is `<name>-<6 characters>`.
- `profiles`: the only ones `agent()` may use, the first its default. An unknown one refuses the start.
- `size`: `agent`, `small`, `medium`, `large` or `xlarge` (table in dyna-images); else the room's `size` setting.
- `repo`: the room's default repository unless given. A run with none has no `worktree` or `merge()`.

A refused start names the setting; `dyna_workflows` lists what is off. Root changes it with
`dyna workflow set` (dyna-admin), others ask their parent. A switch turned off mid-run throws.

## Collect what workers and commands return

A failed call throws an `Error` with `.step` and `.reason`. An unknown option fails that step.

- `args` is parsed already, `null` without `--args`: `JSON.parse(args)` throws on an object.
- `agent()` returns final text, or with `schema` the parsed JSON the worker submits as a JSON file.
  With `worktree: true` it returns `{step, text, branch, commit, files}`, or
  `{step, output, branch, commit, files}` with `schema`: read `r.issues` or `r.output.issues`.
  Workers do not start their own workers.
- `agent()` throws on an unlisted profile, a timeout (seconds; default 5 hours unless the room sets it),
  no final text without `schema`, no valid submit in three tries with one, or a failed worktree push.
- `run()` holds the build lock; `cwd` is relative to the main tree unless absolute. A string runs in
  `sh -c`, a list directly. It returns `{code, stdout, stderr, durationMs}`, the last 64 KiB per stream.
  `check` defaults to `true`: nonzero exit throws `exit code N: <stderr>`; with `check: false` read `code`.
  A timeout throws either way.
- `parallel()` and `pipeline()` do not throw: failure becomes `{failed: true, step, reason}`; in a
  pipeline it skips the item's later stages. A stage gets `(previous, item, index)`.
- `workflow(path, args)` nests one level, ignores the child's `meta`, and reruns on every call.
- `post()` returns `{event, seq}`, once however often the script replays. A run reports only to its parent:
  any other mention fails the step. Omitting `data` differs from `null`.
- `label` names the step in `failedSteps`, `merged`, `conflicts` and `.step`; nothing is read from a step's name.
  The default is the kind and 80 characters, so set distinct labels.
- `status(text)` keeps the newest 20 lines of up to 200 characters. It is not a step and does not replay.
- `Date.now()`, `Math.random()` and `new Date()` without an argument throw: pass time or a seed in `args`.
  The VM has no timers, files or network. A step not awaited is cancelled when the script returns, and fails.

An uncaught throw ends the run `failed`. A return ends it `done`, however many steps failed.
A return value or step result over 1 MiB as JSON fails it.

## Read how a run ended

- A returned string is the starter's reply, plus `Failed steps (N): <names>` when any failed.
- Other values are reply data with `Workflow <name> finished.`: compact UTF-8 JSON, number spellings kept,
  HTML-sensitive characters escaped. Up to 1024 bytes appears in the input; above, `data.json` is attached
  at `/box/attachments/<seq>/data.json`. `post()` data arrives the same way. Nothing is truncated.
- A throw gives `workflow <name>: <reason>`, a stop `Workflow <name> stopped.`.
- `dyna workflow status RUN` shows result, step states, failure reason and script version, and newest merge.
  `dyna workflow list` gives run IDs, sessions and pinned commits. Return the worker output the caller needs.

## Merge worktrees and test the merge

- `worktree: true` gives a worker its own tree and branch `<run session branch>-<12 hex>` from the pinned
  commit (`commit` in `workflow list`). Its work, except ignored files, is committed and pushed;
  `files` lists what changed, `[]` for nothing. The tree is removed; later steps see a branch.
- Without `worktree`, workers share the main tree; its uncommitted files are lost on replacement or resume.
- `merge(results)` merges each `commit`, in order, from `base` (the pinned commit by default) into
  `<run session branch>-run` and returns `{branch, commit, merged, conflicts}`. A conflict or failed
  `parallel` entry lands in `conflicts`; misuse or git failure throws. A later merge replaces the run branch;
  `{base: earlier.commit}` builds on an earlier one.
- `merge()` reads `commit`, not the tip of `branch`: after a push, pass `{...result, commit: pushed}`.
  No rebase or repair: hand a conflict to an `agent()` in a worktree whose prompt names both branches.

The main tree stays pinned. Test the merge in a temporary worktree, in one `run()` a replay cannot split.
Use the repository's test command; `test.code` is 0 when the merge passes:

```js
const dir = `/tmp/check-${merged.commit}`
const test = await run(`git fetch origin ${merged.branch} && git worktree add --detach ${dir} ${merged.commit} &&
  (cd ${dir} && go test ./...); s=$?; git worktree remove --force ${dir}; exit $s`, { check: false, label: 'test merge' })
```

## What a worker knows, and why it waits

- Each `agent()` is a fresh conversation with the shared system-prompt core, AGENTS.md, skills, prompt and schema.
  AGENTS.md changes reach a running worker as `[Context update]` frames; `dyna_*` sections, `args` and earlier
  steps do not, so pass paths, branches and results in the prompt.
- A box holds one worker per 512 MiB (4 to 64 by `size`). A worker waits for a box slot, then its profile's
  `maxConcurrent`; the room's `maxWorkers` can cap a run lower, read when its box starts. `timeout` starts once
  it holds both; waiting on a profile holds a box slot. Root changes `maxWorkers` (dyna-admin); others ask their parent.
- One build runs at a time: `run()` and workers' `cargo`, `go`, `npm`, `make` and similar share one lock;
  time queued for it does not count against a timeout.

## Steer, stop, reload or resume a run
- A post mentioning the run's session reaches every worker running then, before its next model request.
  The script reads nothing; with no worker running the post stays undelivered.
- One worker: `dyna workflow steer RUN STEP TEXT`, or workflow steer with run, step and text. STEP is a
  label or unique key prefix; status lists steps. Steering changes neither its key nor replay.
- `dyna workflow pause RUN [STEP]` holds the run, or one worker, before its next model request; `unpause` lets it go on.
  A run-wide pause also holds new agent() and run() calls. Steering waits for release. Pause survives restarts.
  RUN is a run ID or session name. The starter, its ancestors and owner control the run.
- Stop: session stop on the run's session, or `dyna session stop SESSION`; status then says `stopped`.
  Running steps fail with `the run ended first`; `--resume RUN` carries on from finished steps.

Reload is for a running run, resume for an ended one. A key hashes kind, prompt or command,
options other than `lane`, effective profile and enclosing `workflow()` step. A finished key returns its stored result;
changed prompts or options rerun that step. Changing `meta.profiles[0]` reruns default-profile steps.
Two identical calls are two steps. Name the commit in a reviewer's prompt so its verdict matches the code.

- `dyna workflow reload RUN FILE` fixes a prompt or adds steps while finished work stays. A running step the
  new script calls keeps its worker: no second one starts; one it drops runs on until the run ends.
- `dyna workflow run FILE --resume RUN` takes an ended run of the same room and repository. A resume is a new session
  with the same pinned commit. Failed, cut-off and `workflow()` steps rerun; main-tree files are lost.
  Worktree branches are fetched back (a deleted one fails); it can push through any chain of resumes.

## Shape a run

Useful shapes: write, review (the prompt names the commit) and fix for a fixed number of rounds; several workers
each trying to refute one finding; finders in rounds until rounds in a row find nothing new.
`pipeline()` overlaps stages; a barrier pays when a stage needs all earlier results. A run that samples says
what it left out. A dry run of one group finds wrong prompts cheaply. Give writers separate files.
A worker that skips everything still reports done: check `files.length` or a schema field. A `done` run
is not proof of finished work: `conflicts`, `merged` and failed steps say what happened.
Do not prohibit the tools a worker needs to build and verify its change.

## Start a script from events
Root can attach a script to room events, with one run at a time per key and saved state. Read [events.md](events.md).
