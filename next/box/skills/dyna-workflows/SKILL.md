---
name: dyna-workflows
description: Use when you write, test or resume a Dyna workflow, or reload a running one, a script that fans work out to workers in one box. It gives every script call's return and failure, reload and resume, worktrees and merge, and what a worker knows.
---

# Dyna workflows

A workflow is a script that fans work out to workers in one box. `dyna workflow run FILE [--args JSON] [--resume RUN]`, or the `workflow` tool, starts it as a child session, a crew, and `dyna workflow reload RUN FILE` changes a running one. Any session starts one unless the owner's workflow settings say otherwise, and a refused call names the setting that refused it. Its end, whether a return, a throw or a stop, reaches whoever started it as a reply, so a run needs no polling.

The owner's workflow settings, per room with a global default, can turn off `agent()` with `worktree`, `merge()`, `run()`, `workflow()`, `post()` and resume, limit the runs going and the workers in a run, allow only some profiles, and set the default box size and timeout. A call a setting refuses throws, or fails the start, naming the setting. The `dyna_workflows` section lists what the room changes from the defaults.

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
- `agent(prompt, {profile, schema, label, phase, lane, timeout, worktree})`: one worker, a PiG session. Returns its final text; with `schema`, the parsed value; with `worktree: true`, `{step, text, branch, commit, files}`, or `{step, output, branch, commit, files}` with `schema`, so a schema field is `r.issues`, or `r.output.issues` in a worktree. Throws on an unknown profile, a timeout (seconds, default 5 hours or the room's default timeout), no final text, or three replies that miss the schema.
- `run(command, {cwd, env, timeout, check, label, phase, lane})`: one command in the main tree, holding the build lock. A string runs in `sh -c`, a list directly. Returns `{code, stdout, stderr, durationMs}`, the last 64 KiB of each stream. `check` defaults to `true`, so a nonzero exit throws `exit code N: <stderr>`; with `check: false` the object carries `code`. A timeout throws either way.
- `parallel(thunks)`: starts every thunk at once and returns results in input order. It does not throw: a failure becomes `{failed: true, step, reason}`.
- `pipeline(items, ...stages)`: each item goes through the stages on its own, and a stage gets `(previous, item, index)`. Same failure entries, and a failed item skips its later stages.
- `merge(results, {base, label, phase})`: merges each result's `commit`, in order, from `base` (the pinned commit by default) into `<your branch>-run` (your branch is named in your instructions). Returns `{branch, commit, merged, conflicts}`. A conflict leaves that step out and does not throw. A failed entry lands in `conflicts` with its reason. A later merge starts from `base` again, so `{base: earlier.commit}` builds on an earlier one. `merge()` reads `commit`, not the tip of `branch`: after a push to a result's branch, `{...result, commit: pushed}` carries the new work.
- `workflow(path, args)`: runs one script from the checkout and returns its value. Nesting is one level.
- `post(to, text, data, {lane})`: posts as the crew to its parent and returns `{event, seq}`. A crew reports only to its parent, the session that started it, or root when the owner did, so `to` is `parent` or that session's name or ID; the engine refuses any other mention and the step fails. `data` joins the text as a JSON block.
- `phase(title)` sets the stage of later steps, and the `phase` option sets one step's. `lane` names the subject a step is about, as in `{lane: g.id}`. Nothing is read from a step's name: the run page's flowchart has a node per phase and draws each lane's path between them, loops included, and its timeline labels a step by its lane. Without lanes, phases connect in the order they first ran.
- `status(text)` sets the run's one line of what is happening, shown under its name on the run page and in the Runs list, as in ``status(`Wave 3: ${landed} of ${total} landed`)``. It is not a step: the run keeps the latest line and the 19 before it, and a resumed script writes its lines again as it reaches them. Without one, the page counts steps, as `75 of 76 steps · 1 running`.
- `log(...values)` and `console.*` write to the run.
- `Date.now()`, `Math.random()` and `new Date()` with no argument throw, because a rerun has to make the same calls. A bare `Date()` throws the engine's TypeError, and `globalThis.Date.now()` and `globalThis.Math.random()` reach the frozen built-ins, because the guard is for honest scripts, not a boundary.
- A step the script does not await is cancelled when the script returns, and it fails: a script reads the result of every `agent()` it needs, by awaiting it.

A failed call throws an `Error` with `.step` and `.reason`. An uncaught throw ends the run as `failed`. A script that returns ends `done`, however many steps failed; its value, up to 1 MiB, is the reply.

## Reload and resume

Reload is for a running run, resume for an ended one. Both go by step key: a hash of the step's kind, prompt or command, options other than `lane`, profile and enclosing `workflow()` step, not its order. A key that matches a finished step returns the stored result, so a changed prompt, command or option reruns that step alone, and a kept step takes the lane the script now gives it.

`dyna workflow reload RUN FILE`, or the tool's `reload` action, gives a running run a new script. The starter, a session above it and the owner reload one.

- The same crew, box, main tree, branches and journal carry on. The crew stops the old script and runs the new one from its start.
- A key whose step is running joins it: the call waits for the worker already at work, and no second one starts. A new key starts a step. A running step the new script does not call runs on until the run ends, and then fails with `the run ended first`.
- `meta.name`, `meta.repo`, `meta.size` and `meta.gpu` stay as the run has them, and a reload that changes one is refused. `profiles` and `description` may change, and the run's models follow the new profiles.
- The run keeps every version of its script, and each step records the version that started it.

`--resume RUN` names an ended run of the same room and repository. A resume is a new session, possibly on another host, with the same pinned commit.

- Failed steps and steps the end cut off run again. Main-tree files are not kept. Worktree branches are, and the new run can push to the branches of the runs it resumes, through any chain of resumes.
- A step reruns only when its own key changes. A reviewer whose prompt is unchanged keeps its old verdict after the code under it changed; a prompt that names the commit it reviews ties the verdict to that code.

## Workers

- Each `agent()` is a new PiG session with the session's instructions, one paragraph on being a worker, its prompt and the schema. It has no memory of other steps, no `args`, no feed and no view of other workers, so paths, branches, rules and earlier output reach it only through the prompt.
- It has the session's tools: files, `bash`, `job`, `codemode`, the `dyna` tools and git with the crew's push rights. Nothing wakes a worker: a `bash` command past 30 seconds becomes a job that it reads with `job output`.
- With `worktree: true` it gets its own tree and branch from the pinned commit. What it leaves is committed and pushed before the step is stored, and `files` lists what changed, `[]` for nothing. Without it, workers share the main tree, whose uncommitted files a lost box or a resume drops. Two writers editing one place conflict at merge.
- A worker waits for a box slot, then its profile's `maxConcurrent`; `timeout` starts once it holds both, so a step waiting on its profile holds a box slot. One build runs at a time across `run()` and the workers.

## Stopping

`session stop` on the crew stops the run. Finished steps stay, running ones fail with `the run ended first`, the end reaches the starter as a reply, and the crew closes itself.

## Shaping a run

A run is worth its cost when it covers more than one context holds, checks its own findings, or takes several independent angles before committing. A small task is cheaper done directly.

`pipeline()` is the default for work in stages: each item moves to its next stage as soon as it is ready, so wall time is the slowest single chain, not the sum of each stage's slowest. A barrier, `await parallel(...)` between stages, earns its wait only when a stage needs every earlier result at once: deduplicating across all findings, stopping early when there are none, or comparing items with each other. Flattening, mapping or filtering happens inside a stage and needs no barrier.

```js
const reviewed = await pipeline(groups,
  g => agent(`Write ${g.id}`, { worktree: true, label: `write ${g.id}`, schema: WRITE }),
  w => w.files.length ? agent(`Review commit ${w.commit} on ${w.branch}`, { label: `review ${w.step}`, schema: REVIEW }) : { empty: true })
```

Patterns, which compose:

- **Write, review, fix:** a writer in a worktree, a reviewer whose prompt names the commit, and a fixer that gets the findings and the branch. The fixer's result carries its own commit, and `merge()` takes that. A fixed round count bounds the loop.
- **Adversarial verify:** several workers each try to refute one finding, and the finding survives when most fail to. This keeps a plausible but wrong finding out.
- **Different lenses:** when a finding can fail in more than one way, each verifier takes one lens, such as correctness, security, or whether it reproduces. Different lenses catch what repeating one lens misses.
- **Judge panel:** several independent attempts from different starting points, scored by separate judges, then built from the best with the runners-up's good ideas. It suits a wide solution space.
- **Loop until dry:** finders run in rounds until a set number of rounds in a row turn up nothing new, deduplicated against everything seen so far, not only against what passed. A fixed count misses the tail.
- **Several search methods:** workers each search a different way, by file, by content, by name or by history, because each misses what the others find.
- **Completeness check:** a last worker asks what is missing, such as an unread source, an unrun method or an unchecked claim, and its answer becomes the next round.
- **No silent caps:** a run that samples or takes the top N says what it left out with `log()` and in its result, so a partial pass does not read as a full one.

Scale follows the ask: a quick check takes a few workers and one verifier each, and a thorough audit takes more finders, three to five verifiers and a closing synthesis. Profile limits bound how many run at once. A stronger profile for reviewing and a cheaper one for writing is a common split.

## Gotchas

- A worker that skips everything still reports done, because it ends with text. Reading `files.length` or a schema field before moving on catches it.
- A rule worded too tightly stops work: "read and write files and use git only" made every writer give up in 40 seconds.
- A dry run of one group finds a wrong prompt or result shape for the cost of one worker.
- A `done` run is not proof of finished work: `conflicts`, `merged` and failed steps say what happened.
