# Dyna workflows

## Workflow with a foreground join

Save this as `check.js`. Workers see only their prompt, profile, and working directory. Include all needed context.

```js
const schema = {
  type: 'object',
  required: ['summary'],
  properties: { summary: { type: 'string' } },
  additionalProperties: false,
}
const results = await parallel([
  () => agent('Review decoder bounds. Return a summary.', { schema }),
  () => agent('Review decoder error handling. Return a summary.', { schema }),
])
return { results }
```

```bash
dyna run check.js
```

Keep `dyna run` in the foreground when its result is the work. Without `schema`, `agent()` returns final text. With it, Dyna validates JSON and retries invalid output.

## Profiles

Run `dyna profiles list --json` before choosing profiles. Stats range from 1–10; higher is better:

- `taste`: choosing the solution.
- `diagnostics`: finding what is wrong with a solution (reviews, debugging), with few false positives.
- `intelligence`: executing a given solution on hard multi-step work.
- `cost`: higher is cheaper.

Use descriptions to confirm the choice. Omitting `profile` selects the default workflow profile, or the first eligible profile if none is marked default.

`profiles` contains enabled workflow profiles: `name`, `description`, `harness`, `model`, `taste`, `diagnostics`, `intelligence`, `cost`, `default`, `use`, `maxConcurrent`, `maxCallsPerRun`, and live read-only `remaining` (`null` means unlimited).
Only enabled profiles whose uses include `workflows` appear; naming another rejects the call.

`maxConcurrent` queues excess calls. `maxCallsPerRun` is a lifetime logical-call cap; exceeding it rejects immediately and fails the run. Accepted calls still finish and journal for `--resume`.
Global defaults are `max(2, min(16, CPU cores - 2))` concurrent workers and 1000 calls. Set `--max-concurrent` and `--max-agents` before launch.
Run `dyna profiles check` for installed binaries and output capabilities. Read `dyna guide steering` for harness delivery.

## Runtime

Scripts use JavaScript with top-level `await` and `return`; the return becomes the JSON result.
They are not Node.js programs: no TypeScript, `require`, modules, filesystem, networking, or arbitrary environment access. Workers handle filesystem work.

Workflow workers cannot delegate, start runs or sessions, or read any form of `dyna guide`. Dyna disables native delegation: Claude runs without the Agent tool.
Workers receive `DYNA_MODE=workflow`, `DYNA_WORKER_RUN`, and `DYNA_WORKER_AGENT`. They can read run status and record progress with `dyna journal`.
Read `dyna guide delegation` for persistent sessions and `dyna guide recovery` after provider failure.

Optional metadata goes first:

```js
export const meta = {
  name: 'review-change',
  description: 'Review a target',
  phases: [{ title: 'Review' }],
}
```

`meta` is a convention, not a validated runtime schema. Without `--name`, Dyna uses `meta.name`. Actual progress groups use `phase(title)` or `agent`'s `phase` option.

## Public JavaScript API

### `agent(prompt, opts?)`

Returns a promise for worker text or validated JSON. Options:

- `profile`: eligible profile name.
- `label`: progress label and stable resume identity; use unique author-controlled labels for matching jobs.
- `id`: graph node identity; duplicates are diagnosed, never merged.
- `phase`: explicit group; use inside concurrent callbacks to prevent interleaving global groups.
- `schema`: JSON Schema; three attempts total. Corrections resume the harness session, or retry the full prompt if resume is unavailable. Scalars must occupy the whole response or a fenced JSON block; objects and arrays may appear in prose.
- `cwd`: worker directory; defaults to run `--dir`, then shell directory.
- `timeout`: positive seconds; default five hours. Explicit script/profile values have a 30-minute minimum.
- `isolation: { type: 'worktree', retention?: 'changed' | 'all' | 'none' }`: detached worktree at committed `HEAD`. Default `changed` retains changed trees; `all` retains all; `none` journals changes before removal.

Isolation excludes uncommitted changes. Dyna never merges retained work. Hand later workers an engine-verified path from `worktrees()`, selected by author-controlled label; never trust worker-reported paths. Parallel writers need disjoint scopes or worktrees.

### `parallel(thunks)`

Starts all thunks, waits for all, and preserves input order; rejected calls log errors and become `null`. Child failures do not reject the barrier. Handle `null` results. Thunks can contain whole per-item stage chains.

### `pipeline(items, ...stages)`

Runs items concurrently through stages with no stage barrier. Each callback receives `(previousResult, originalItem, index)`. Failure logs an error, returns `null` for that item, and skips later stages.
Use inline callbacks for exact graph attribution; callbacks passed by identifier are best-effort. Exact attribution includes calls after awaits, nested `parallel()` thunks, literal spreads, conditional/sequence expressions, static array/object selections, and immediately invoked inline factories.
Awaited async factories and simple block-bodied factories also retain attribution. Direct recursive self-calls preserve item context. After awaits, `.call`, `.apply`, `.bind`, and `Reflect.apply` lose exact attribution and the receiver.

### `post(to, text)`

Synchronously stores and delivers a room post as `run:<id>`. `to` is a mention or mention array; non-empty `text` has a 16 KiB limit. Resolution errors throw; check returned `error` and deliveries after storage. Keep bulk output in artifacts.
Read `dyna guide posting` for mentions and delivery fields.

```js
const sent = post('@root', 'Review ready. See the run result.')
if (sent.error) throw new Error(sent.error)
```

### `workflow(nameOrRef, args?)`

Returns a child script's result in an isolated runtime. Absolute paths resolve directly; relative paths resolve beside the caller, then under `--dir`. `.js` is optional.
Bare names search `--workflows-dir` (default `$XDG_CONFIG_HOME/dyna/workflows`), then `<work-dir>/examples`.
A child must not call `workflow()`. Siblings may run concurrently. Child agents share parent concurrency and lifetime limits; composition itself consumes no call. Child artifacts live under `workflows/<child-id>/`; `dyna runs show` displays child groups.

### Globals and progress

- `phase(title)`: sets the default group for later agents.
- `log(message)`: emits durable `t=log` with `provenance: "workflow"`; truncates at 4000 characters. Engine diagnostics use `provenance: "system"`. Stderr output respects `--quiet`.
- `sleep(ms)`: pauses the script for milliseconds.
- `args`: JSON parsed from `--args`, or `undefined`; nested JSON strings are not parsed.
- `process`: frozen shim with `cwd()` (run `--dir`), `platform`, `arch`, and frozen `env` containing only present `HOME`, `USER`, `XDG_CONFIG_HOME`, `XDG_DATA_HOME`, `LANG`.
- `profiles`: profile snapshot described above.
- `worktrees()`: engine-verified retained records with `label`, `path`, and `retainedAt`.

## Workflow shape: parallel by default

Name the work list before writing the script. Run independent calls in parallel; sequence calls only when later prompts need earlier results.
`parallel()` has no item-count cap; concurrency limits queue calls rather than truncate inputs.
Stream per-item stages. Use barriers only for cross-item decisions. Reduce cost with fewer calls or cheaper profiles.
Partition implementation into disjoint file sets, then stream each writer into review and verification.

### Workflow templates

```bash
dyna templates list --json
dyna templates copy adversarial-review --to /tmp/my-review.js --json
dyna run /tmp/my-review.js --args '{"target":"src/"}'
```

Run `dyna templates --help` for save, show, seed, and overwrite options. Templates live under `--templates-dir` (default `$XDG_CONFIG_HOME/dyna/templates`). Install and update seed missing starters and update unchanged seeded copies; they preserve user edits. `dyna templates` reads the registry without executing source.
Copies require `.js`, reject symlinks, and require `--overwrite` to replace files. Templates cannot run by name or resolve through `workflow()`.

- `adversarial-review`: independent review lenses, then synthesis.
- `judge-panel`: independent judgments, then a decision.
- `research-design`: discovery, verification, and design.

## Run history

`dyna runs show` keeps full results. Printed strings over 1,500 characters become file pointers with previews.
Use `dyna runs archive --older-than 24h`; running and paused runs and resume ancestors of active runs are never moved. Archives live under `~/.local/share/dyna/archive/runs/`; symlinks preserve absolute paths. Archived IDs support show, output, graph, and `--resume`.
Use `dyna runs list --archived` or `--all`, and `dyna runs unarchive <id>` to restore.
Inspect retained trees with `dyna git worktrees list --check`; remove with `dyna git worktrees remove`.
