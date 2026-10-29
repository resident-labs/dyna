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
They are not Node.js programs: no TypeScript, `require`, modules, filesystem, networking, or arbitrary environment access. Workers handle filesystem work, and `run()` runs commands.

Each `agent()` call starts a regular agent session that cannot start subagents or runs. Claude runs without the Agent tool. The run closes each session on resolution; sessions stay out of member lists and groups, and results go to the run. Workers record progress with `dyna journal`.
To watch an agent, open its session from its row on the run page. Steer it with `dyna post @run:<id>/agent:<n> "<message>"`. An older run shows only the journal and the final result.
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

Use `run()` for deterministic work, including polling for files, statuses, or receipts. Use `agent()` for model judgment. Set timeouts and handle failures.

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

### `run(command, opts?)`

Runs a deterministic command with no model turn and returns `{ code, stdout, stderr, json, durationMs, attempts, shellId, logPath }`. An array runs without a shell; a string runs through `sh -c`.
Options: `cwd` (default: run `--dir`), `env` (added to the run's environment), `label` (stable resume identity), `phase`, `lane`, `stage` (as for `agent()`), `timeout` seconds (default five hours), `retries` (default 0, because commands can have side effects), `check`, and `parse: 'json'`.
A non-zero exit, a timeout, or output that is not JSON rejects, so `parallel()` yields `null` and `pipeline()` drops the item. With `check: false` the script gets the result instead.
`stdout` and `stderr` keep the first 64 KiB; `logPath` has the full standard output. Each call is a managed shell of the run (see `dyna shell`), runs on the engine host, and stops when the run is canceled or ends. The run page shows its command, duration, exit code, and log tail.
`--resume` reuses a finished command's result, reattaches to a shell still running, and reruns a failed one. Include `check` and a unique `label` in the call you want to match.

```js
const gate = await run(['python3', 'bridge.py', 'landed', '--unit', unit], { label: 'wait-' + unit, parse: 'json', timeout: 6 * 3600 })
await agent('Review the landing: ' + JSON.stringify(gate.json), { schema })
```

For long-running workflows, use `run()` to read a changing manifest at each dispatch. Dyna does not reload workflow variables or source automatically.

### `job(spec)`

Runs a command through the queue. Returns `{id,node,state,exitCode,log}`. Resume waits for the original job ID by spec, call and branch. Ambiguous replay rejects; stopping cancels it.

```js
const check = await job({ command: ['make', 'test'], labels: ['os=linux'] })
```

### `parallel(thunks)`

Starts all thunks, waits for all, and preserves input order; rejected calls log errors and become `null`. Child failures do not reject the barrier. Handle `null` results. Thunks can contain whole per-item stage chains.

### `pipeline(items, ...stages)`

Runs items concurrently through stages with no stage barrier. Each callback receives `(previousResult, originalItem, index)`. Failure logs an error, returns `null` for that item, and skips later stages.
Use inline callbacks for exact graph attribution; callbacks passed by identifier are best-effort. Exact attribution includes calls after awaits, nested `parallel()` thunks, literal spreads, conditional/sequence expressions, static array/object selections, and immediately invoked inline factories.
Awaited async factories and simple block-bodied factories also retain attribution. Direct recursive self-calls preserve item context. After awaits, `.call`, `.apply`, `.bind`, and `Reflect.apply` lose exact attribution and the receiver.

### `post(to, text, data?)`

Stores and delivers a room post synchronously under the workflow's name. `to` is a mention or array; non-empty `text` is limited to 16 KiB. Resolution errors throw; check returned `error` and deliveries. Keep bulk output in artifacts.
Read `dyna guide posting` for mentions and delivery fields.

Pass facts as `data` (object or array), or `post(to, { text, data })`; never inline JSON in `text`. The feed shows `data` as a collapsed key and value view. Recipients read it as a fenced JSON block after `text`; the limit counts it.

```js
const sent = post('@root', 'Review ready. See the run result.')
if (sent.error) throw new Error(sent.error)
post('@integrator', 'Landing request after review.', { unit: 'CH', head: '31cdb141', evidence: '/tmp/receipt.json' })
```

### `workflow(nameOrRef, args?)`

Returns a child script's result in an isolated runtime. Absolute paths resolve directly; relative paths resolve beside the caller, then under `--dir`. `.js` is optional.
Bare names search `--workflows-dir` (default `$XDG_CONFIG_HOME/dyna/workflows`), then `<work-dir>/examples`.
A child must not call `workflow()`. Siblings may run concurrently. Child agents share parent concurrency and lifetime limits; composition itself consumes no call. Child artifacts live under `workflows/<child-id>/`; `dyna runs show` displays child groups.

### Globals and progress

- `phase(title)` or `phase(title, {lane, stage})`: sets the default group; lane and stage place it on the run grid.
- `log(message)`: emits durable `t=log` with `provenance: "workflow"`; truncates at 4000 characters. Engine diagnostics use `provenance: "system"`. Stderr output respects `--quiet`.
- `sleep(ms)`: pauses the script for milliseconds.
- `args`: JSON parsed from `--args`, or `undefined`; nested JSON strings are not parsed.
- `process`: frozen shim with `cwd()` (run `--dir`), `platform`, `arch`, and frozen `env` containing only present `HOME`, `USER`, `XDG_CONFIG_HOME`, `XDG_DATA_HOME`, `LANG`.
- `profiles`: profile snapshot described above.
- `worktrees()`: engine-verified retained records with `label`, `path`, and `retainedAt`.

## Workflow shape: parallel by default

Name the work list before writing the script. Run independent calls in parallel; sequence dependent calls. `parallel()` queues excess calls without truncating inputs. Stream per-item stages; use barriers for cross-item decisions. Reduce cost with fewer calls or cheaper profiles. Partition writers into disjoint file sets, then stream each into review and verification.

### Keep builds and tests out of the fan-out

Fan out writing and reviewing freely. Run builds and tests in one agent of the run, or in the pull requests afterwards, reviewed outside the workflow. Concurrent builds exhaust CPU, memory and disk. Workers skip builds and tests unless the prompt asks.
`run()` commands inside `parallel()` queue behind `--max-commands` (default 4, per run), so a script cannot start 200 builds at once. Commands do not count against `--max-agents`.

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
