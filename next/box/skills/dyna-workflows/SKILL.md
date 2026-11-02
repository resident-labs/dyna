---
name: dyna-workflows
description: Use when you write, test or resume a Dyna workflow, a script that fans work out to Pi workers in one box. It gives every script call with its exact return and failure, resume and step keys, worktrees and merge, stopping a run, and what a worker knows.
---

# Dyna workflows

`dyna workflow run FILE [--args JSON] [--resume RUN]` or the `workflow` tool starts a script. Only the owner or a room's root can start one. The run is a child session, a crew, with one box. Each `agent()` is a Pi process in that box. The script runs in Node with no modules, files, network or timers, and it has top-level `await` and `return`. Its return value is the run's result, up to 1 MiB. `Date.now()`, `Math.random()` and `new Date()` with no argument throw, because they would differ when the script runs again.

```js
export const meta = { name: "sweep", profiles: ["terra", "deep"], size: "large" };
const fixes = await parallel(args.groups.map((g) => () => agent(`Fix ${g}`, { worktree: true, label: g })));
const merged = await merge(fixes);
return { commit: merged.commit, conflicts: merged.conflicts.length };
```

## Inputs

- `args`: the request field is JSON text, and the engine refuses text that is not JSON. The script's global `args` is that value already parsed, so `JSON.parse(args)` throws on an object. It is `null` when none was given.
- `meta`: the first statement, with only comments before it. It is a pure literal that the engine reads without running the script, and it refuses unknown keys, unknown profiles and unknown sizes before a box exists. The script can read it as `meta`.
- `meta.name` (required): letters, digits, dots, underscores or hyphens, at most 57, starting with a letter or digit. The session is `<name>-<six characters of the run ID>`.
- `meta.profiles` (required): the first is the default for `agent()` and the session's own profile. A profile removed after the start is left out of the run, so a call to it fails.
- `meta.size`: `agent`, `small`, `medium`, `large` or `xlarge`. Without it the size is the repository's `.dyna/images/<name>/size`, or `medium`.
- `meta.repo`: one of the room's repositories, by default the room's default. A run with no repository cannot use `worktree` or `merge()`.
- `meta.description`: shown with the run.

## Calls

A call that fails throws an `Error` with `.failed`, `.step` and `.reason`, and the journal records the step as failed. An uncaught throw ends the run as `failed`. Unknown options fail the step.

| Call | Returns | Fails |
|---|---|---|
| `agent(prompt, {profile, schema, label, phase, timeout, worktree})` | The final text as a string. With `schema`, the parsed JSON value itself. With `worktree: true`, `{step, text, branch, commit, files}`, or `{step, output, branch, commit, files}` with `schema`. | Throws on an unknown profile, a timeout (seconds, default 5 hours), a worker that ends with no text, or three replies that miss the schema. |
| `run(command, {cwd, env, timeout, check, label, phase})` | `{code, stdout, stderr, durationMs}`, with the last 64 KiB of each stream. A string runs in `sh -c` and a list runs directly, in the main tree unless `cwd` says otherwise. | `check` defaults to `true`: a nonzero exit throws `exit code N: <stderr>` and the script gets no result object. `check: false` returns the object with its `code`. A timeout throws either way. |
| `merge(results, {base, label, phase})` | `{branch, commit, merged, conflicts}`. | A conflict does not throw. Throws on anything but `worktree` results or a git failure. |
| `parallel(thunks)` | An array in input order. `thunks` are functions, and all start at once. | Does not throw. A failed entry is `{failed: true, step, reason}`. |
| `pipeline(items, ...stages)` | An array in input order of each item's last value. A stage gets `(previous, item, index)`. | Does not throw. A failed entry is `{failed: true, step, reason}`, and that item's later stages are skipped. |
| `workflow(path, value)` | The child script's return value. `path` is from the checkout, `value` is its `args`, and nesting is one level. | Throws with the child's reason. |
| `post(to, text, data)` | `{event, seq}`. `data` joins the text as a JSON block. | Throws with the engine's reason. |
| `phase(title)`, `log(...values)`, `console.*` | Nothing. `phase` groups later steps on the run page. | Do not fail. |

Three shapes read differently. `agent(p, {schema})` gives `r.issues`. `agent(p, {schema, worktree: true})` gives `r.output.issues`. An entry of `parallel` or `pipeline` can be `{failed: true, ...}`, so `r.failed` comes first. `step` in a result is the label, or `agent: <start of the prompt>`.

`to` in `post` is one mention or a list of them: a session name, `root`, `parent`, `children`, `siblings` or `room:name`, with or without `@`. A session ID is not a mention and fails with `unknown session @ses_...`. An empty list posts without waking anyone. The same step posts once, however often the script runs again.

## Worktrees and merge

`worktree: true` gives a worker its own git worktree on `v2/<session>-<12 characters of the step key>`, from the run's pinned commit. When the worker ends, the box commits what it left, except ignored files and without hooks, pushes the branch, and only then stores the step. The tree goes with the step, so a branch and a commit are what remain. A worker without `worktree` shares the main tree, and a lost box or a resume does not keep that tree's uncommitted files. `files` lists the paths changed since the pinned commit, and `[]` means the worker changed nothing.

`merge()` runs in a temporary tree. It merges each result's `commit` in order from `base`, by default the pinned commit, and pushes `v2/<session>-run`, which only ever holds clean merges. The main tree does not move, so a test fetches that branch inside `run()`.

- `merged` lists step names, and a result with no changes is among them.
- A conflict leaves that step out and the next merge goes on. Its entry in `conflicts` is `{step, branch, files}`, and the run's end counts it.
- A failed entry from `parallel` lands in `conflicts` as `{step, files: [], reason}`.
- A later `merge()` starts from `base` again and replaces the run branch, so `{base: earlier.commit}` builds on an earlier one.
- `merge()` reads `commit`, not the tip of `branch`. After a push to a result's branch, `{...result, commit: pushed}` carries the new work.
- Two writers editing one place conflict. A worker given both branches can repair that.

## Resume

`--resume RUN` names an ended run (`done`, `failed` or `stopped`) of the same room and repository. A running run is refused, because it resumes by itself after a box loss. The request gives the script and `args` again, and the engine keeps neither from the old run.

A resume is a new session: a new ID, a new name, a new box, possibly on another host.

- **Kept:** every finished step with its result, the pinned commit, and the image while the host holds its tag. A host without the tag builds what the repository holds now.
- **Not kept:** failed steps and steps cut off by the end, which run again; `workflow()` steps, which run again so their inner steps look themselves up; main tree files; usage, which counts only the new run's spending.
- **Branches:** a kept step's result still names the earlier session's branch, and the box fetches that branch. A resumed run can push the session branches of the run it resumes, through any chain of resumes, in the same room and repository. Other pushes get `not owned by this session` with the rule. A step that reruns uses the new session's branch.

A step key is a SHA-256 of the kind, the prompt or command or payload, the options, the effective profile and the enclosing `workflow()` step's key. The call's order, the rest of the script, `args` and `phase()` are not in it. Identical calls in one execution get an ordinal.

- A changed prompt, command or post text reruns that step.
- Any changed option reruns it, `label`, `timeout` and `schema` included.
- A reordered `meta.profiles` reruns every `agent()` that names no `profile`.
- A changed `path` or `value` of `workflow()` reruns every step inside it.
- A changed result reruns `merge()`, whose key holds its results.
- A later step reruns only when its own key changes. A prompt that quotes a changed output or commit differs, so that step reruns, and the change carries on downstream the same way. A later prompt with fixed text keeps its stored result even though the code under it changed, which is how a reviewer returns a verdict on stale code. A prompt that holds the commit it reviews avoids that.

## Stopping and knowing it ended

`session stop` on the crew's session stops a run. The script and workers end, finished steps stay, and running steps fail with `the run ended first`. `workflow run` returns the session ID as `session`.

A run's end needs no polling. Whether the run is `done`, `failed` or `stopped`, the final reply reaches whoever started it, and root wakes on it when root started the run. It holds the return value, or `Workflow NAME stopped.`, or the thrown reason, and a line naming failed steps. The crew then closes itself, so `session close` on it answers that the session is closed.

A run counts as `done` when its script returned, however many steps failed along the way. A box that cannot set up is a failed session: `session.failed` tells the parent, `session restart` retries, and the run waits. `workflow status RUN` and `workflow list` show a run's state and result and its steps, without the steps' results. A step's result reaches a session only through the script's return value or `post()`.

## What a worker has

Each `agent()` is a new Pi process on its profile's model. It knows the session's instructions, one paragraph on being a worker, and its prompt. It has no memory of earlier steps, no `args`, no view of other workers, and no room feed, so paths, branches, rules and earlier output reach it only through the prompt. The schema joins the prompt by itself.

A worker has a session's tools: file tools, `bash`, `job`, `codemode`, the `dyna` tools and CLI, the room's skills and MCP servers, and git with the crew's push rights. What it posts, it posts as the crew. Nothing wakes a worker: a `bash` command past 30 seconds becomes a background job, and the worker reads it with `job output` until it ends. Its final text is its result.

## Limits

`parallel` and `pipeline` start everything at once, and each `agent()` waits for room without failing.

- The box runs one worker per 512 MiB of memory: `agent` 4, `small` 8, `medium` 16, `large` 32, `xlarge` 64.
- A profile's `maxConcurrent` bounds that profile in this crew, and zero means no limit.
- A step takes a box slot, then its profile slot, so a step waiting on its profile holds a box slot.
- `timeout` starts once a worker has both slots, and it leaves out time queued for the build lock.
- One build runs at a time. `run()` holds the lock for its whole command, and a worker's `cargo`, `go`, `npm`, `make` and similar builds queue for it.

## What failed runs taught

- A small dry run of one group costs one worker and finds a wrong shape before a long phase reaches it.
- A rule worded too tightly stops work. "Read and write files and use git only" made every worker give up in 40 seconds, because each one read it as forbidding the tools its task needed.
- A worker that skips everything still reports done, because a step records only that the worker ended with text. Every step of a run can pass with no work in it. A script that reads `files.length`, a schema field or a `run()` result before it moves on sees that.
- A `done` run is not proof of finished work. `failed` steps, `conflicts` and `merged` say what happened.
