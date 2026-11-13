---
name: dyna-workflows
description: Use when you write, run, resume or reload a Dyna workflow script, or a task needs many agents coordinated in code.
---

# Write and run Dyna workflows

A workflow is a JavaScript script that a crew, one child session with one box, runs to start workers,
commands and merges. Every call is a step stored under a key hashed from what the call is, so a
restarted box, a reload or a resume gets each unchanged finished step's result back without rerunning it.

## Write and start a script

Save this complete script as `sweep.js`, set `profiles` from `dyna_profiles`, and start it as below.

```js
export const meta = { name: 'sweep', profiles: ['fast'], size: 'large', description: 'One change per group' }
const WRITE = { type: 'object', required: ['summary'], properties: { summary: { type: 'string' } } }
const written = await parallel(args.groups.map(g => () => agent(`In this repository, ${g.task}`,
  { worktree: true, label: `write ${g.id}`, lane: g.id, schema: WRITE })))
const merged = await merge(written.filter(w => !w.failed && w.files.length), { label: 'merge' })
return { branch: merged.branch, merged: merged.merged, conflicts: merged.conflicts,
  empty: written.filter(w => !w.failed && !w.files.length).map(w => w.step) }
```

```sh
dyna workflow run sweep.js --args '{"groups":[{"id":"a","task":"fix the typos in README.md."}]}'
```

- Only comments may come before `meta`. An unknown key, or no `name` or `profiles`, refuses the start.
- `name`: letters, digits, `.`, `_` or `-`, at most 57; the crew session is `<name>-<6 characters>`.
- `profiles`: the only ones `agent()` may use, the first its default. An unknown one refuses the start.
- `size`: `agent`, `small`, `medium`, `large` or `xlarge` (table in dyna-images); else the room's `size` setting.
- `repo`: by default the room's default repository. A run with none has no `worktree` or `merge()`.

A refused start names the setting; `dyna_workflows` lists what is off. Root changes it with
`dyna workflow set` (dyna-admin), others ask their parent. A switch turned off mid-run throws.

## Collect what workers and commands return

A failed call throws an `Error` with `.step` and `.reason`. An option a call does not take fails that step.

- `args` is `null` without `--args`. It is already parsed: `JSON.parse(args)` throws on an object.
- `agent()` returns the final text, or with `schema` the parsed JSON the worker submits as a JSON file.
  With `worktree: true` it returns `{step, text, branch, commit, files}`, or
  `{step, output, branch, commit, files}` with `schema`, so a field is `r.issues` or `r.output.issues`.
- `agent()` throws on a profile not in `meta.profiles`, on a timeout (seconds; default the room's
  workflow timeout, 5 hours unless set), on no final text (without `schema`), on no valid submit in
  three tries (with one), and when a worktree's push fails.
- `run()` holds the build lock; `cwd` is relative to the main tree unless it is absolute. A string runs
  in `sh -c`, a list directly. It returns `{code, stdout, stderr, durationMs}`, the last 64 KiB of each
  stream. `check` defaults to `true`, so a nonzero exit throws `exit code N: <stderr>`; with
  `check: false` read `code`. A timeout throws either way.
- `parallel()` and `pipeline()` do not throw: a failure becomes `{failed: true, step, reason}`, and in a
  pipeline a failed item skips its later stages. A stage gets `(previous, item, index)`.
- `workflow(path, args)` nests one level, ignores the child's `meta`, and reruns on every call.
- `post()` returns `{event, seq}`, once however often the script replays.
  A crew reports only to its parent: any other mention fails the step. Omitting `data` differs from `null`.
- `label` names the step in `failedSteps`, `merged`, `conflicts` and `.step`, and
  nothing is read from a step's name; the default is the kind and 80 characters, so set distinct labels.
- `status(text)` keeps the newest 20 lines of up to 200 characters. It is not a step and does not replay.
- `Date.now()`, `Math.random()` and `new Date()` with no argument throw, because a rerun makes the same
  calls; pass a time or a seed in `args`. The VM has no timers, files or network.
- A step the script does not await is cancelled when the script returns, and fails.

An uncaught throw ends the run `failed`. A return ends it `done`, however many steps failed; a return
value or step result over 1 MiB as JSON fails it.

## Read how a run ended

- A returned string is the starter's reply text, plus `Failed steps (N): <names>` when any failed.
- Any other value is the reply's data, with the text `Workflow <name> finished.`: compact UTF-8 JSON,
  number spellings kept, HTML-sensitive characters as `\u` escapes. Up to 1024 bytes it is printed in
  the input; above, it is the attachment `data.json` at the `/box/attachments/<seq>/data.json` path the
  input names. `post()` data arrives the same way. Nothing is truncated.
- A throw gives the error `workflow <name>: <reason>`, a stop `Workflow <name> stopped.`.
- `dyna workflow status RUN` (or the tool's `status` action) shows the result, each step's state,
  failure reason and script version, and the newest merge; `dyna workflow list` gives run IDs, crew
  sessions and pinned commits. A worker's words are not readable from a box, so return what is needed.

## Merge worktrees and test the merge

- `worktree: true` gives a worker its own tree and branch `<crew branch>-<12 hex>` from the pinned
  commit (`commit` in `workflow list`). What it left, except ignored files, is committed and pushed;
  `files` lists what changed, `[]` for nothing. The tree is then removed; later steps see a branch.
- Without `worktree`, workers share the main tree, whose uncommitted files a replaced box or a resume drops.
- `merge(results)` merges each `commit`, in order, from `base` (the pinned commit by default) into
  `<crew branch>-run`, pushes it and returns `{branch, commit, merged, conflicts}`. A conflict or a
  failed `parallel` entry lands in `conflicts` as a failed step; it throws only on misuse or a git
  failure. A later `merge()` replaces the run branch; `{base: earlier.commit}` builds on an earlier one.
- `merge()` reads `commit`, not the tip of `branch`: after a push to a result's branch,
  `{...result, commit: pushed}` carries the new work. No rebase and no repair: hand a conflict to an
  `agent()` in a worktree whose prompt names both branches.

The main tree stays on the pinned commit, so test a merge in a temporary worktree, in one `run()` that
a replay cannot split. Use the repository's test command; `test.code` is 0 when the merge passes:

```js
const dir = `/tmp/check-${merged.commit}`
const test = await run(`git fetch origin ${merged.branch} && git worktree add --detach ${dir} ${merged.commit} &&
  (cd ${dir} && go test ./...); s=$?; git worktree remove --force ${dir}; exit $s`, { check: false, label: 'test merge' })
```

## What a worker knows, and why it waits

- Each `agent()` is a new session with the shared core of the system prompt, AGENTS.md, the skills, its
  prompt and the schema: no `dyna_*` context, no `args`, no memory of other steps, so paths, branches,
  rules and earlier output reach it only through the prompt.
- A box holds at most one worker per 512 MiB (4 to 64 by `size`). A worker waits for a box slot, then
  its profile's `maxConcurrent` (`dyna_profiles`); the room's workflow `maxWorkers` can cap a run lower,
  read when its box starts (root changes it, dyna-admin; others ask their parent). `timeout` starts once a worker holds both, so a step
  waiting on its profile holds a box slot.
- One build runs at a time in the box: `run()` and workers' `cargo`, `go`, `npm`, `make` and similar
  share one lock, and time queued for it does not count against a timeout.

## Steer, stop, reload or resume a run

- Steer: a post that mentions the run's session reaches every worker running then, before its next
  model request. The script reads nothing, and with no worker running the post stays undelivered.
- Stop: the session tool's `stop` action on the crew, or `dyna session stop CREW`; `dyna workflow status RUN`
  then says `stopped`. Running steps fail with `the run ended first`; `--resume RUN` carries on from the finished steps.

Reload is for a running run, resume for an ended one. Both match steps by key: a hash of the kind,
prompt or command, options other than `lane`, effective profile and enclosing `workflow()` step. A
finished key returns its stored result, so a changed prompt or option reruns that step alone, and a new
`meta.profiles[0]` reruns every step that used the default. Two identical calls are two steps. Name
the commit in a reviewer's prompt, or its old verdict survives a change to the code.

- `dyna workflow reload RUN FILE` (or the tool's `reload` action) fixes a prompt or adds steps while
  finished work stays. A running step the new script still calls keeps its worker, so
  no second one starts; one it drops runs on until the run ends.
- `dyna workflow run FILE --resume RUN` takes an ended run of the same room and repository.
  A resume is a new session, possibly on another host, with the same pinned commit. Failed steps,
  `workflow()` steps and cut-off steps rerun; main-tree files are lost. Worktree branches are fetched
  back (a deleted one fails its call); it can push to branches of runs through any chain of resumes.

## Shape a run

`pipeline()` is the default for stages:
wall time is the slowest chain, not the sum of each stage's slowest. A barrier, `await parallel(...)`
between stages, pays only when a stage needs every earlier result at once: deduplicating, stopping
early, comparing. Useful shapes: write, review (the prompt names the commit) and fix, for a fixed
number of rounds; several workers each trying to refute one finding; finders in rounds until a set
number of rounds in a row find nothing new. A run that samples or takes the top N says what it left out.

- A worker that skips everything still reports done. Check `files.length` or a schema field.
- Do not prohibit the tools a worker needs to build and verify its change.
- A dry run of one group finds a wrong prompt or result shape for the cost of one worker.
- Two writers editing one place conflict at merge, so give writers separate files.
- A `done` run is not proof of finished work: `conflicts`, `merged` and failed steps say what happened.
