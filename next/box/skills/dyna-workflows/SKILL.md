---
name: dyna-workflows
description: Use when you write, test or resume a Dyna workflow, a script that fans work out to Pi workers in one box. It gives every script call's return and failure, resume, worktrees and merge, and what a worker knows.
---

# Dyna workflows

A workflow is a script that fans work out to Pi workers in one box. `dyna workflow run FILE [--args JSON] [--resume RUN]`, or the `workflow` tool, starts it as a child session, a crew. Only the owner or a room's root starts one. Its end, whether a return, a throw or a stop, reaches whoever started it as a reply, so a run needs no polling.

Every script begins with `export const meta = {...}`: a pure literal (no variables, calls or interpolation) that the engine reads before any box exists.

```js
export const meta = { name: 'sweep', profiles: ['terra', 'opus'], size: 'large', description: 'One PR per group' }
```

- `name` (required): letters, digits, `.`, `_` or `-`, at most 57.
- `profiles` (required): the first is `agent()`'s default.
- `size`: `agent`, `small`, `medium`, `large` or `xlarge`, one worker per 512 MiB: 4, 8, 16, 32 or 64.
- `repo`: one of the room's repositories, by default the room's default. Without one, `worktree` and `merge()` are unavailable.

The usual shape is writers in worktrees, a check that each wrote something, and one merge:

```js
const written = await parallel(args.groups.map(g => () =>
  agent(`Implement ${g.id}: ...`, { worktree: true, label: `write ${g.id}`, schema: WRITE })))
const real = written.filter(w => !w.failed && w.files.length)
const merged = await merge(real)
return { merged: merged.merged, conflicts: merged.conflicts, empty: written.filter(w => !w.failed && !w.files.length).map(w => w.step) }
```

## Script globals

- `args`: the request's JSON, already parsed, or `null`. `JSON.parse(args)` throws on an object.
- `agent(prompt, {profile, schema, label, phase, timeout, worktree})`: one Pi worker. Returns its final text; with `schema`, the parsed value; with `worktree: true`, `{step, text, branch, commit, files}`, or `{step, output, branch, commit, files}` with `schema`, so a schema field is `r.issues`, or `r.output.issues` in a worktree. Throws on an unknown profile, a timeout (seconds, default 5 hours), no final text, or three replies that miss the schema.
- `run(command, {cwd, env, timeout, check, label, phase})`: one command in the main tree, holding the build lock. A string runs in `sh -c`, a list directly. Returns `{code, stdout, stderr, durationMs}`, the last 64 KiB of each stream. `check` defaults to `true`, so a nonzero exit throws `exit code N: <stderr>`; with `check: false` the object carries `code`. A timeout throws either way.
- `parallel(thunks)`: starts every thunk at once and returns results in input order. It does not throw: a failure becomes `{failed: true, step, reason}`.
- `pipeline(items, ...stages)`: each item goes through the stages on its own, and a stage gets `(previous, item, index)`. Same failure entries, and a failed item skips its later stages.
- `merge(results, {base, label, phase})`: merges each result's `commit`, in order, from `base` (the pinned commit by default) into `v2/<session>-run`. Returns `{branch, commit, merged, conflicts}`. A conflict leaves that step out and does not throw. A failed entry lands in `conflicts` with its reason. A later merge starts from `base` again, so `{base: earlier.commit}` builds on an earlier one. `merge()` reads `commit`, not the tip of `branch`: after a push to a result's branch, `{...result, commit: pushed}` carries the new work.
- `workflow(path, args)`: runs one script from the checkout and returns its value. Nesting is one level.
- `post(to, text, data)`: posts as the crew and returns `{event, seq}`. `to` is one name or a list: a session name, `root`, `parent`, `children`, `siblings` or `room:name`. A session ID is not a mention and fails. `data` joins the text as a JSON block.
- `phase(title)` groups later steps on the run page. `log(...values)` and `console.*` write to the run.
- `Date.now()`, `Math.random()` and `new Date()` with no argument throw, because a rerun has to make the same calls.

A failed call throws an `Error` with `.step` and `.reason`. An uncaught throw ends the run as `failed`. A script that returns ends `done`, however many steps failed; its value, up to 1 MiB, is the reply.

## Resume

`--resume RUN` names an ended run of the same room and repository. A resume is a new session, possibly on another host, with the same pinned commit.

- A step's key is a hash of its kind, prompt or command, options, profile and enclosing `workflow()` step, not its order. A key that matches a finished step returns the stored result, so a changed prompt, command or option reruns that step alone.
- Failed steps and steps the end cut off run again. Main-tree files are not kept. Worktree branches are, and the new run can push to the branches of the runs it resumes, through any chain of resumes.
- A step reruns only when its own key changes. A reviewer whose prompt is unchanged keeps its old verdict after the code under it changed; a prompt that names the commit it reviews ties the verdict to that code.

## Workers

- Each `agent()` is a new Pi process with the session's instructions, one paragraph on being a worker, its prompt and the schema. It has no memory of other steps, no `args`, no feed and no view of other workers, so paths, branches, rules and earlier output reach it only through the prompt.
- It has the session's tools: files, `bash`, `job`, `codemode`, the `dyna` tools and git with the crew's push rights. Nothing wakes a worker: a `bash` command past 30 seconds becomes a job that it reads with `job output`.
- With `worktree: true` it gets its own tree and branch from the pinned commit. What it leaves is committed and pushed before the step is stored, and `files` lists what changed, `[]` for nothing. Without it, workers share the main tree, whose uncommitted files a lost box or a resume drops. Two writers editing one place conflict at merge.
- A worker waits for a box slot, then its profile's `maxConcurrent`; `timeout` starts once it holds both, so a step waiting on its profile holds a box slot. One build runs at a time across `run()` and the workers.

## Stopping

`session stop` on the crew stops the run. Finished steps stay, running ones fail with `the run ended first`, the end reaches the starter as a reply, and the crew closes itself.

## Gotchas

- A worker that skips everything still reports done, because it ends with text. Reading `files.length` or a schema field before moving on catches it.
- A rule worded too tightly stops work: "read and write files and use git only" made every writer give up in 40 seconds.
- A dry run of one group finds a wrong prompt or result shape for the cost of one worker.
- A `done` run is not proof of finished work: `conflicts`, `merged` and failed steps say what happened.
