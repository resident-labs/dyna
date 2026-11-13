---
name: dyna-workflows
description: Use when you write, run, resume or reload a Dyna workflow script, read why a run failed, or a task needs many agents coordinated in code.
---

# Dyna workflows

A workflow is a JavaScript script that a crew, one child session with one box, runs to start workers (`agent()`), commands (`run()`) and merges. Every call is a step stored in a journal under a key hashed from what the call is, so a restarted box, a reload or a resume gets each unchanged finished step's result back without running it again.

## Write a script

A script starts with `export const meta = {...}`, a pure literal (no variables, calls or `${}`) that the engine reads before any box exists. Only comments may come before it. An unknown key, or no `name` or `profiles`, refuses the start.

```js
export const meta = { name: 'sweep', profiles: ['terra', 'opus'], size: 'large', description: 'One PR per group' }
```

- `name`: letters, digits, `.`, `_` or `-`, at most 57. The crew session is named `<name>-<6 characters>`.
- `profiles`: `agent()` may use only these; the first is its default. An unknown profile refuses the start.
- `size`: `agent`, `small`, `medium`, `large` or `xlarge` (2, 4, 8, 16 or 32 GiB), one worker per 512 MiB: 4, 8, 16, 32 or 64 at once. Without it, the room's workflow `size` setting, else the engine's default.
- `repo`: one of the room's repositories, by default the room's default. A run with none has no `worktree` or `merge()`.
- `gpu`: GiB of GPU memory for the crew's box; workers started by `agent()` get none.

The usual shape is writers in worktrees, a check that each wrote something, and one merge:

```js
const written = await parallel(args.groups.map(g => () =>
  agent(`Implement ${g.id}: ...`, { worktree: true, label: `write ${g.id}`, lane: g.id, schema: WRITE })))
const real = written.filter(w => !w.failed && w.files.length)
const merged = await merge(real)
return { merged: merged.merged, conflicts: merged.conflicts, empty: written.filter(w => !w.failed && !w.files.length).map(w => w.step) }
```

## What each call returns and how it fails

A failed call throws an `Error` with `.step` and `.reason`. An option a call does not take fails that step.

- `args`: the run's `--args` JSON, already parsed, or `null`. `JSON.parse(args)` throws on an object.
- `agent(prompt, {profile, schema, label, phase, lane, timeout, worktree})` returns the worker's final text; with `schema`, the parsed JSON; with `worktree: true`, `{step, text, branch, commit, files}`, or `{step, output, branch, commit, files}` with `schema`, so a schema field is `r.issues`, or `r.output.issues` in a worktree. With `schema`, the worker submits a JSON file with its `submit` tool, so a large result lives in the file and its final text is not read. It throws on a profile not in `meta.profiles`, on a timeout (seconds; default the room's workflow timeout, 5 hours unless set), on no final text (without `schema`), on no valid submit in three tries (with one), and when a worktree's push fails.
- `run(command, {cwd, env, timeout, check, label, phase, lane})` runs in the main tree (`cwd` relative to it), holding the build lock. A string runs in `sh -c`, a list directly. Returns `{code, stdout, stderr, durationMs}`, the last 64 KiB of each stream. `check` defaults to `true`, so a nonzero exit throws `exit code N: <stderr>`; with `check: false` the object carries `code`. A timeout throws either way.
- `parallel(thunks)` starts every thunk at once and returns results in input order. It does not throw: a failure becomes `{failed: true, step, reason}`.
- `pipeline(items, ...stages)`: each item goes through the stages on its own, and a stage gets `(previous, item, index)`. Same failure entries, and a failed item skips its later stages.
- `merge(results, {base, label, phase})` (no `lane`): see Worktrees and merge. Returns `{branch, commit, merged, conflicts}`.
- `workflow(path, args)` runs a script from the main tree in its own VM and returns its value; a throw there throws here. It nests one level, the child's `meta` is ignored (it uses the run's profiles), and it reruns on every call, so an edited child takes effect while its unchanged steps return stored results.
- `post(to, text, data, {lane})` posts as the crew and returns `{event, seq}`, once however often the script replays. A crew reports only to its parent, the session that started it, or root when the owner did, so `to` is `parent` or that session's name or ID; any other mention fails the step. `data` is a JSON value kept apart from `text`; leaving it out differs from `null`. Delivery is below.
- `phase(title)` sets the stage of later steps, and the `phase` option one step's. `lane` names the subject a step is about, as in `{lane: g.id}`. Both are stored on the step and drawn on the run page; nothing is read from a step's name.
- `label` names the step in `failedSteps`, `merged`, `conflicts` and `.step`. Without it the name is the kind and the first 80 characters of the prompt or command, so give every step a distinct label.
- `status(text)` sets the run's one line of what is happening, which `workflow list` and `workflow status` return, as in ``status(`Wave 3: ${landed} of ${total} landed`)``. It is not a step and does not replay: the run keeps the latest line (at most 200 characters) and the 19 before it, and a resumed script writes its lines again as it reaches them.
- `log(...values)` and `console.*` add a line to the journal: strings as they are, arrays and objects as JSON.
- `Date.now()`, `Math.random()` and `new Date()` with no argument throw, because a rerun has to make the same calls. Pass a time or a seed in `args`. The VM has no timers, no files and no network.
- A step the script does not await is cancelled when the script returns, and fails.

An uncaught throw ends the run `failed`. A script that returns ends `done`, however many steps failed; a return value over 1 MiB as JSON fails it, as does a step result over 1 MiB.

## What the starter gets

- A returned string is the reply's text, followed by `Failed steps (N): <names>` when any failed.
- Any other value is the reply's data, with the text `Workflow <name> finished.` The data is compact UTF-8 JSON with number spellings kept and HTML-sensitive characters as `\u` escapes. Up to 1024 bytes it is printed in the input; above that it arrives as the attachment `data.json`, at the `/box/attachments/<seq>/data.json` path the input names. `post()` data reaches its recipient the same way. Nothing is truncated.
- A throw gives the error `workflow <name>: <reason>`, and a stop the text `Workflow <name> stopped.`, both with the failed steps.
- `dyna workflow status RUN` (or the tool's `status` action) shows the result, the status lines, each step's state and failure reason, and the newest merge; `dyna workflow list` gives run IDs, crew branches and pinned commits. What a worker said is not readable from a box, so a script returns or commits whatever someone needs later.

## Worktrees and merge

- `worktree: true` gives a worker its own tree and branch `<crew branch>-<12 hex>` from the pinned commit, the commit the crew's checkout started on (`commit` in `workflow list`). When the worker ends, what it left, except ignored files, is committed and pushed before the step is stored; `files` lists what changed against the pinned commit, `[]` for nothing. The tree is removed, so later steps see a branch and a commit, not a folder.
- Without `worktree`, workers share the main tree, whose uncommitted files a lost box or a resume drops. Two writers editing one place conflict at merge, so give writers separate files.
- `merge(results)` merges each result's `commit`, in order, from `base` (the pinned commit by default) into `<crew branch>-run` and pushes it. A conflict leaves that step out, lands in `conflicts` with its `files` and `branch`, counts as a failed step, and does not throw. A failed entry from `parallel` lands in `conflicts` with its `reason`. It throws only on misuse (anything but worktree results) or a git failure.
- A later `merge()` starts from `base` again and replaces the run branch, so `{base: earlier.commit}` builds on an earlier one. `merge()` reads `commit`, not the tip of `branch`: after a push to a result's branch, `{...result, commit: pushed}` carries the new work.
- No rebase and no repair: hand a conflict to an `agent()` in a worktree whose prompt names both branches. The main tree does not move, so a script tests a merge by fetching its branch inside `run()`.

## What a worker knows

- Each `agent()` is a new PiG session with the shared core of the system prompt, the AGENTS.md files, the skills, one paragraph on being a worker, its prompt and the schema. It has no git, delegation or communication sections, no `dyna_*` context, no memory of other steps, no `args` and no view of other workers, so paths, branches, rules and earlier output reach it only through the prompt.
- It has the session's tools: files, `bash`, `job`, `codemode`, the `dyna` tools and git with the crew's push rights. Nothing wakes a worker: a `bash` command past 30 seconds becomes a job that it reads with `job output`.
- A worker waits for a box slot, then its profile's `maxConcurrent` (in `dyna_profiles`); `timeout` starts once it holds both, so a step waiting on its profile holds a box slot. One build runs at a time in the box: `run()` and workers' `cargo`, `go`, `npm`, `make` and similar take one lock, and time queued for it does not count against a timeout. Cargo builds in every tree share the main tree's `target`.

## Start, stop and change a run

- Start: `dyna workflow run FILE [--args JSON] [--resume RUN]` or the tool's `run` action. A refusal names the setting that refused it, and `dyna_workflows` lists what the room turned off. The settings are the owner's; root changes them with `dyna workflow set` (skill dyna-admin), and any other session asks its parent. A crew and its workers start no run; a script nests one with `workflow()`.
- A switch the owner turns off reaches a running script at its next call, which throws naming the setting.
- Stop: `session stop` on the crew. Finished steps stay, running ones fail with `the run ended first`, the end reaches the starter, and the crew closes.
- A lost box needs nothing: a new box runs the script again and stored steps return at once.

## Reload and resume

Reload is for a running run, resume for an ended one. Both go by step key: a hash of the step's kind, prompt or command, options other than `lane`, effective profile and enclosing `workflow()` step, not its order. A matching finished key returns its stored result, so a changed prompt, command or option reruns that step alone, and a kept step takes the lane the script now gives it. Changing `meta.profiles[0]` reruns every step that used the default. Two identical calls are two steps, numbered in call order.

`dyna workflow reload RUN FILE`, or the tool's `reload` action, gives a running run a new script. The starter, a session above it and the owner reload one.

- The same crew, box, main tree, branches and journal carry on. The crew stops the old script and runs the new one from its start.
- A key whose step is running joins it: the call waits for the worker already at work, and no second one starts. A new key starts a step. A running step the new script does not call runs on until the run ends, then fails with `the run ended first`.
- `meta.name`, `meta.repo`, `meta.size` and `meta.gpu` stay as the run has them, and a reload that changes one is refused. `profiles` and `description` may change, and the run's models follow the new profiles.
- The run keeps every version of its script, and each step records the version that started it.

`--resume RUN` names an ended run of the same room and repository; a running one is refused. A resume is a new session, possibly on another host, with the same pinned commit.

- Failed steps, `workflow()` steps and steps the end cut off run again. Main-tree files are not kept. Worktree branches are fetched back (a deleted one fails its call), and the new run can push to the branches of the runs it resumes, through any chain of resumes.
- A step reruns only when its own key changes. A reviewer whose prompt is unchanged keeps its old verdict after the code under it changed; a prompt that names the commit it reviews ties the verdict to that code.

## Shaping a run

A run is worth its cost when it covers more than one context holds, checks its own findings, or takes several independent angles before committing. A small task is cheaper done directly.

`pipeline()` is the default for work in stages: each item moves to its next stage as soon as it is ready, so wall time is the slowest single chain, not the sum of each stage's slowest. A barrier, `await parallel(...)` between stages, earns its wait only when a stage needs every earlier result at once: deduplicating across all findings, stopping early when there are none, or comparing items. Flattening, mapping or filtering happens inside a stage and needs no barrier.

```js
const reviewed = await pipeline(groups,
  g => agent(`Write ${g.id}`, { worktree: true, label: `write ${g.id}`, lane: g.id, schema: WRITE }),
  w => w.files.length ? agent(`Review commit ${w.commit} on ${w.branch}`, { label: `review ${w.step}`, schema: REVIEW }) : { empty: true })
```

Patterns, which compose:

- **Write, review, fix:** a writer in a worktree, a reviewer whose prompt names the commit, and a fixer given the findings and the branch. The fixer's result carries its own commit, which `merge()` takes. A fixed round count bounds it.
- **Adversarial verify:** several workers each try to refute one finding; it survives when most fail to.
- **Different lenses:** each verifier takes one lens, such as correctness, security or whether it reproduces.
- **Judge panel:** several independent attempts, scored by separate judges, then built from the best with the runners-up's good ideas. It suits a wide solution space.
- **Loop until dry:** finders run in rounds until a set number of rounds in a row find nothing new, deduplicated against everything seen so far, not only what passed. A fixed count misses the tail.
- **Several search methods:** by file, by content, by name, by history; each misses what the others find.
- **Completeness check:** a last worker asks what is missing (an unread source, an unrun method, an unchecked claim), and its answer becomes the next round.
- **No silent caps:** a run that samples or takes the top N says what it left out with `log()` and in its result.

Scale follows the ask: a quick check takes a few workers and one verifier each; a thorough audit takes more finders, three to five verifiers and a closing synthesis. A strong profile for reviewing and a cheap one for writing is common.

## Mistakes that cost a run

- A worker that skips everything still reports done, because it ends with text. Check `files.length` or a schema field before moving on.
- A rule worded too tightly stops work: "read and write files and use git only" made every writer give up in 40 seconds.
- A dry run of one group finds a wrong prompt or result shape for the cost of one worker.
- A `done` run is not proof of finished work: `conflicts`, `merged` and failed steps say what happened.
