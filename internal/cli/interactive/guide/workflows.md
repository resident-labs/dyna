# Dyna workflows

A workflow is a JavaScript program that root writes to run many agents in the background, on any enabled profile and harness, mixed in one run. Use workflows for large jobs: sweeps, migrations of hundreds of files, audits, ports, and verify-and-fix loops. Use a subagent for one check or anything one agent can do. Only the owner or root can start a workflow.

## Example

Save this as `check.js`. Workers see only their prompt, profile, and working directory, so include the context they need.

```js
const results = await parallel([
  () => agent('Audit decoder bounds and report findings.'),
  () => agent('Audit decoder error handling and report findings.'),
])
return { results }
```

Run `dyna workflow run check.js` in the foreground to join the results. Handle `null` results before using them.

## Profiles

Run `dyna profiles list --json` before choosing profiles. Higher ratings are better: `taste` measures solution choice, `diagnostics` finding faults, `intelligence` execution, and `cost` cost efficiency. Only enabled profiles with the `workflows` use are available. Omit `profile` to use the default workflow profile, or the first eligible profile.

`maxConcurrent` queues excess calls. `maxCallsPerRun` caps logical calls for the run, including resume. Defaults are `max(2, min(16, CPU cores - 2))` concurrent workers and 1000 calls. Set `--max-concurrent` and `--max-agents` before launch. Run `dyna profiles check` for installed binaries and output capabilities.

## Running workflows

Scripts support top-level `await` and `return`; the return becomes the JSON result. They are not Node.js programs: modules, filesystem and networking are unavailable. Use workers for file work and `run()` for commands. Run independent calls in parallel and sequence dependent calls. Partition parallel writers into disjoint files or worktrees. Keep builds and tests out of the fan-out; run them in one agent or in pull requests afterwards.

Workers are leaf sessions. Dyna closes each when its call resolves. Open a worker from the run page to watch it, or steer it with `dyna post @run:<id>/agent:<n> "<message>"`. Workers record progress with `dyna journal`. Read `dyna guide recovery` after provider failure.

## Safe automatic replay

Automatic replay is off unless the author declares `const meta = { deterministic: true }`. The workflow must make the same calls in the same order for the same inputs and recorded results. Never use clocks, randomness, mutable external state, or attempt-scoped variables such as `DYNA_RUN_ID` for call choices or replayed results. Overlapping asynchronous calls cannot be replayed safely. Dyna checks inputs and the manifest before replay. It reports why it stopped when checks fail. Manual `--resume` uses the older cache and can repeat effects. See the README for details.

## JavaScript API

### `agent(prompt, opts?)`

Returns worker text, or JSON validated against `schema` with up to three attempts. Options include `profile`, `label` (unique stable resume identity), `id` (graph identity), `phase`, `cwd`, `timeout` in seconds, and `isolation: { type: 'worktree', retention: 'changed' | 'all' | 'none' }`. The default timeout is five hours; explicit values have a 30-minute minimum. Isolation uses committed `HEAD`. Dyna never merges retained work; select verified paths by label from `worktrees()`.

### `run(command, opts?)`

Runs a command on the engine host and returns `{ code, stdout, stderr, json, durationMs, attempts, shellId, logPath }`. Arrays run directly; strings run through `sh -c`. Options include `cwd`, `env`, `label`, `phase`, `lane`, `stage`, `timeout`, `retries`, `check`, and `parse: 'json'`. Failures reject unless `check: false`. Output is limited to 64 KiB per stream; `logPath` holds full standard output. Resume reuses completed results, reattaches to running shells, and reruns failures. Commands queue behind `--max-commands` (default 4).

### `job(spec)`

Runs a command through the node queue and returns `{id,node,state,exitCode,log}`. Resume waits for the original job; ambiguous replay rejects. Stopping the workflow cancels it.

### `parallel(thunks)`

Starts all thunks and waits for all. Results preserve input order; failures become `null` and do not reject the barrier.

### `pipeline(items, ...stages)`

Runs items concurrently through stages without a stage barrier. Each callback receives `(previousResult, originalItem, index)`. Failure returns `null` for that item and skips later stages. Use inline callbacks for exact graph attribution.

### `post(to, text, data?)`

Posts synchronously to mentions under the workflow's name. Check the returned error and deliveries. Pass structured facts as `data`, not inline JSON. Read `dyna guide posting` for details.

### `workflow(nameOrRef, args?)`

Runs a child script in an isolated runtime and returns its result. Paths resolve beside the caller, then under `--dir`; bare names also search `--workflows-dir` and examples. Children cannot call `workflow()`. They share the parent's worker limits.

### Globals

`phase(title, {lane, stage})` sets a progress group. `log(message)` records a message; `sleep(ms)` pauses. `args` holds JSON from `--args`. `profiles` is the enabled workflow profile snapshot, including limits and remaining calls. `worktrees()` returns verified retained paths. `process` is a frozen shim with `cwd()`, `platform`, `arch`, and a limited `env`.
