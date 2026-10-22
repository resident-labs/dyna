<script lang="ts">
  // CI health: the room's runs as a graph, then a run, then a test. A column
  // is one run: its jobs stacked, the wait for a runner at its foot, and the
  // room's warn and fail lines across. A row is one slow test, and its state
  // is a rule at the edge, a type weight, its place in the list and a line of
  // words, never a coloured chip.
  import Segmented from '../ui/Segmented.svelte'
  import EmptyState from '../ui/EmptyState.svelte'
  import TabHeader from '../rooms/TabHeader.svelte'
  import { api } from '../../api.js'
  import {
    bandNames,
    bandPx,
    bandShare,
    barPct,
    breachLimit,
    capRows,
    budgetWithAllow,
    budgetWord,
    duration,
    failed,
    filterTests,
    graphRuns,
    graphScale,
    jobMs,
    readCIHealth,
    readRunDetail,
    readTestDetail,
    rowLimit,
    runState,
    spark,
    stateLine,
    type CIHealth,
    type CIHealthRun,
    type CIHealthRunDetail,
    type CIHealthTest,
    type TestFilter,
  } from './health.ts'

  interface Props {
    roomId: string
    narrow?: boolean
    changes?: unknown
    onpr?: (pr: number) => void
  }
  let { roomId, narrow = false, changes = null, onpr = () => {} }: Props = $props()

  // The client's call names, typed so the readers get unknown data.
  interface Calls {
    ciHealth(room: string, runs: number): Promise<unknown>
    ciHealthRun(room: string, token: string): Promise<unknown>
    ciHealthTest(room: string, test: string): Promise<unknown>
    roomCIBudget(room: string): Promise<unknown>
    setRoomCIBudget(room: string, budget: Record<string, unknown>): Promise<unknown>
  }
  const calls: Calls = api

  let health = $state<CIHealth | null>(null)
  let error = $state('')
  let note = $state('')
  let run = $state<CIHealthRunDetail | null>(null)
  let test = $state<CIHealthTest | null>(null)
  let loading = $state('')
  let filter = $state<TestFilter>('all')
  let allowOpen = $state(false)
  let allowReason = $state('')
  let allowError = $state('')
  let allowing = $state(false)
  let seq = 0

  const chart = $derived(narrow ? 120 : 176)
  const runs = $derived(health?.runs ?? [])
  const scale = $derived(graphScale(runs, health?.failMs ?? 0))
  // Newest first from the store; the graph draws oldest on the left, and a
  // narrow screen takes the newest few before reversing them.
  const graph = $derived(graphRuns(runs, narrow ? 8 : 0))
  const tests = $derived(health ? filterTests(health.tests, filter) : [])
  const capped = $derived(capRows(tests, rowLimit))
  const breaches = $derived(test ? capRows(test.runs.filter((row) => row.over), breachLimit) : { shown: [], hidden: 0 })
  const bands = $derived(bandNames(runs))
  const over = $derived((health?.tests ?? []).filter((row) => row.breaches > 0).length)
  const warnPx = $derived(bandPx(health?.warnMs ?? 0, scale, chart))
  const failPx = $derived(bandPx(health?.failMs ?? 0, scale, chart))
  const waitScale = $derived(Math.max(1, ...graph.shown.map((item) => item.queueMs)))
  const newest = $derived(graph.newest)
  const filters: [TestFilter, string][] = [
    ['all', 'All'],
    ['recurring', 'Recurring'],
    ['new', 'New'],
    ['quiet', 'Quiet'],
    ['ignored', 'Ignored'],
  ]

  const message = (e: unknown): string => (e instanceof Error ? e.message : String(e))

  async function load(): Promise<void> {
    const mine = ++seq
    try {
      const next = readCIHealth(await calls.ciHealth(roomId, 30))
      if (mine !== seq) return
      if (next) {
        health = next
        error = ''
      } else {
        error = 'The engine sent CI health data this client cannot read.'
      }
    } catch (e) {
      if (mine === seq) error = message(e)
    }
  }

  // Reload when the room changes and when the room's changes move, so a new
  // CI run appears without a reload of the page.
  $effect(() => {
    const room = roomId
    void changes
    void room
    void load()
  })

  async function openRun(token: string): Promise<void> {
    loading = token
    note = ''
    allowOpen = false
    try {
      const detail = readRunDetail(await calls.ciHealthRun(roomId, token))
      if (detail) {
        run = detail
        test = null
      } else {
        note = 'That run could not be read.'
      }
    } catch (e) {
      note = message(e)
    } finally {
      loading = ''
    }
  }

  async function openTest(name: string): Promise<void> {
    loading = name
    note = ''
    allowOpen = false
    try {
      const detail = readTestDetail(await calls.ciHealthTest(roomId, name))
      if (detail) {
        test = detail.test
        run = null
      } else {
        note = 'That test could not be read.'
      }
    } catch (e) {
      note = message(e)
    } finally {
      loading = ''
    }
  }

  function back(): void {
    run = null
    test = null
    note = ''
    allowOpen = false
  }

  async function allow(): Promise<void> {
    if (!test) return
    const reason = allowReason.trim()
    if (!reason) {
      allowError = 'Add the measured time and the reason.'
      return
    }
    allowing = true
    allowError = ''
    try {
      const next = budgetWithAllow(await calls.roomCIBudget(roomId), test.test, reason)
      if (!next) {
        allowError = 'The room budget could not be read.'
        return
      }
      await calls.setRoomCIBudget(roomId, next)
      allowOpen = false
      allowReason = ''
      await load()
      await openTest(test.test)
    } catch (e) {
      allowError = message(e)
    } finally {
      allowing = false
    }
  }

  const runLabel = (token: string): string => {
    const id = token.split('/')[0] ?? token
    return id ? `#${id}` : token
  }

  const longestIn = (jobs: CIHealthRunDetail['jobs']): number => {
    let longest = 0
    for (const job of jobs) {
      longest = Math.max(longest, job.busyMs)
      for (const step of job.steps) longest = Math.max(longest, step.ms)
    }
    return longest
  }

  const stateOf = (row: CIHealthTest): string => (row.state === 'ignored' ? 'allowed' : row.state)
  // The two charts keep their own scale and size; the helpers keep the markup
  // to one expression per attribute.
  const testLine = (row: CIHealthTest) => spark(row.runs, 520, 96, row.limitMs)
  const rowLine = (row: CIHealthTest) => spark(row.runs, 118, 26, row.limitMs)
</script>

<div class="health">
<TabHeader title="CI health" blurb="The room's CI runs against its budget, and the tests that run near the limit." />
{#if note}<p class="err" role="alert">{note}</p>{/if}

{#if run}
  <section class="level" aria-label="Run {runLabel(run.run.token)}">
    <button class="back" onclick={back}>‹ All runs</button>
    <div class="lhead">
      <h2>
        {#if run.run.pr}#{run.run.pr} · {/if}{run.run.workflow}
        <span class="stateword">{runState(run.run)}</span>
      </h2>
      <p class="kv">
        <span>{duration(run.run.busyMs)} busy</span>
        {#if run.run.queueMs}<span>· {duration(run.run.queueMs)} waiting</span>{/if}
        <span>· warn {duration(run.run.warnMs)}, fail {duration(run.run.failMs)}</span>
        <span>· {budgetWord(run.run.busyMs, run.run.warnMs, run.run.failMs)}</span>
        {#if run.run.head}<span>· <span class="mono">{run.run.head.slice(0, 7)}</span></span>{/if}
      </p>
    </div>
    <h3>Jobs and steps</h3>
    <p class="caption">Every bar shares one scale: the longest job or step in this run.</p>
    {#each run.jobs as job (job.id)}
      <div class="bar">
        <span class="bn">{job.name}</span>
        <span class="track" aria-hidden="true"><span class="fill" style:width="{barPct(job.busyMs, longestIn(run.jobs))}%"></span></span>
        <span class="bt">{job.busyMs ? duration(job.busyMs) : job.status || 'no time'}</span>
      </div>
      {#if job.steps.length}
        <div class="steps">
          {#each job.steps as step (step.name)}
            <div class="bar step">
              <span class="bn">{step.name}</span>
              <span class="track" aria-hidden="true"><span class="fill" style:width="{barPct(step.ms, longestIn(run.jobs))}%"></span></span>
              <span class="bt">{duration(step.ms)}</span>
            </div>
          {/each}
        </div>
      {/if}
    {/each}
    {#if run.tests.length}
      <h3>Slowest tests in this run</h3>
      {#each run.tests as row (row.test)}
        <button class="runrow" onclick={() => openTest(row.test)}>
          <span class="rule{row.over ? ' over' : ''}"></span>
          <span class="id"><span class="name">{row.test}</span><span class="meta">{row.package} · limit {duration(row.durationMs > 0 ? (health?.limitMs ?? 0) : 0)}</span></span>
          <span class="times"><b>{duration(row.durationMs)}</b>this run</span>
        </button>
      {/each}
    {/if}
  </section>
{:else if test}
  <section class="level" aria-label="Test {test.name}">
    <button class="back" onclick={back}>‹ All runs</button>
    <div class="lhead">
      <h2>{test.name} <span class="stateword">{stateOf(test)}</span></h2>
      <p class="kv">
        <span>{test.package}</span>
        {#if test.features.length}<span>· {test.features.join(', ')}</span>{/if}
        <span>· {test.breaches} {test.breaches === 1 ? 'breach' : 'breaches'}</span>
      </p>
      <p class="why">{stateLine(test)}</p>
    </div>
    <svg class="wide" viewBox="0 0 520 96" preserveAspectRatio="none" role="img" aria-label="Times of the last stored runs, against the limit">
      <line x1="0" y1={testLine(test).limitY} x2="520" y2={testLine(test).limitY} class="limit" />
      {#if testLine(test).hasLine}<polyline points={testLine(test).points} class="line" />{/if}
    </svg>
    <p class="caption">The dashed line is the limit, {duration(test.limitMs)}. Each point is one stored run, newest on the right.</p>
    <h3>The runs it broke</h3>
    {#if breaches.shown.length === 0}
      <p class="caption">No breach is stored now.</p>
    {:else}
      {#if breaches.hidden > 0}
        <p class="caption">The newest {breaches.shown.length} breaches, of {breaches.shown.length + breaches.hidden} stored.</p>
      {/if}
      <div class="table">
        {#each breaches.shown as row (row.token)}
          <button class="trow" onclick={() => openRun(row.token)} aria-label="Open run {runLabel(row.token)} for PR {row.pr}">
            <span class="mono">#{row.pr}</span>
            <span class="dur">{duration(row.durationMs)}</span>
            <span class="when">{row.at.replace('T', ' ').slice(0, 16)}</span>
          </button>
        {/each}
      </div>
    {/if}
    <div class="acts">
      {#if test.runs[0]}
        {@const newestRun = test.runs[0]}
        <button class="link" onclick={() => openRun(newestRun.token)}>Open the newest run</button>
        {#if newestRun.pr}<span>·</span><button class="link" onclick={() => onpr(newestRun.pr)}>Open PR #{newestRun.pr}</button>{/if}
      {/if}
      {#if test.state !== 'ignored'}<span>·</span><button class="link" onclick={() => { allowOpen = !allowOpen; allowError = '' }}>{allowOpen ? 'Cancel' : 'Allow…'}</button>{/if}
    </div>
    {#if allowOpen}
      <div class="allow">
        <p class="caption">Allowing a test writes this room's allow list, with the reason below. A stored breach stays in the list.</p>
        <input type="text" bind:value={allowReason} placeholder="measured {duration(test.limitMs)} alone; waits on the trial gate" disabled={allowing} aria-label="Reason for allowing {test.test}" />
        {#if allowError}<p class="err">{allowError}</p>{/if}
        <button class="primary" onclick={allow} disabled={allowing}>Allow {test.name}</button>
      </div>
    {/if}
  </section>
{:else}
  {#if error}
    <EmptyState title="CI health unavailable">{error}</EmptyState>
  {:else if !health}
    <div class="loading" aria-busy="true"></div>
  {:else if runs.length === 0}
    <EmptyState title="No CI run yet">A run is recorded when the watcher inspects one of this room's pull requests.</EmptyState>
  {:else}
    <p class="summary">
      {#if newest}newest run {duration(newest.busyMs)} · {runState(newest)}{/if}
      <span>· warn {duration(health.warnMs)}, fail {duration(health.failMs)}</span>
      <span>· {over} over the test limit of {duration(health.limitMs)}</span>
    </p>

    <div class="chart">
    <div class="plot" style:height="{chart}px" role="group" aria-label="Busy time per run, with the warn and fail budgets">
      {#if warnPx > 0}<div class="warn" style:bottom="{warnPx}px">{#if !narrow}<em>warn {duration(health.warnMs)}</em>{/if}</div>{/if}
      {#if failPx > 0}<div class="fail" style:bottom="{failPx}px">{#if !narrow}<em>fail {duration(health.failMs)}</em>{/if}</div>{/if}
      {#each graph.shown as item (item.token)}
        {@const column = bandPx(item.busyMs, scale, chart)}
        <button
          class="col"
          aria-label="Run {runLabel(item.token)}: {duration(item.busyMs)}, {budgetWord(item.busyMs, item.warnMs, item.failMs)}, {runState(item)}"
          title="{runLabel(item.token)} · {duration(item.busyMs)} busy · {runState(item)}"
          onclick={() => openRun(item.token)}
        >
          {#each bands as name, index (name)}
            {#if jobMs(item, name) > 0}<span class="band b{index % 6}" style:height="{column * bandShare(item, name)}px" title="{name} {duration(jobMs(item, name))}"></span>{/if}
          {/each}
          {#if failed(item)}<span class="dot" aria-hidden="true"></span>{/if}
        </button>
      {/each}
    </div>
    </div>
    {#if graph.shown.some((item) => item.queueMs > 0)}
      <div class="waits" role="group" aria-label="Time each run waited for a runner, which the budget does not count">
        {#each graph.shown as item (item.token)}
          {#if item.queueMs > 0}<span class="queued" style:height="{bandPx(item.queueMs, waitScale, 10)}px" title="{runLabel(item.token)} waited {duration(item.queueMs)}"></span>{:else}<span class="queued none"></span>{/if}
        {/each}
      </div>
      <p class="caption">The strip is time each run waited for a runner. The budget does not count it, so it stays out of the column.</p>
    {/if}

    {#if !narrow}
      <p class="caption">One column per run, oldest on the left. A column is the run's measured busy time against this room's budget lines, and the jobs divide it.</p>
      <p class="legend">
        {#each bands as name, index (name)}<span><i class="b{index % 6}"></i>{name}</span>{/each}
        <span><i class="queued"></i>waiting for a runner</span>
        <span><i class="dotmark"></i>failed</span>
      </p>
    {/if}

    <h3 class="tests">Slow tests</h3>
    <Segmented options={filters} value={filter} label="Show tests by state" onchange={(value: TestFilter) => (filter = value)} />
    {#if tests.length === 0}
      <p class="caption">No test in this state.</p>
    {:else}
      {#if capped.hidden > 0}
        <p class="caption">The {capped.shown.length} tests that broke the limit most often, of {tests.length}. Use the filter to narrow them.</p>
      {/if}
      {#each capped.shown as row (row.test)}
        <button class="row {row.state}" onclick={() => openTest(row.test)}>
          <span class="rule"></span>
          <span class="id">
            <span class="name">{row.name}</span>
            <span class="meta">{row.package}{#if row.features.length} · {row.features.join(', ')}{/if} · {stateLine(row)}</span>
          </span>
          {#if !narrow}
            <svg class="spark" viewBox="0 0 118 26" preserveAspectRatio="none" role="img" aria-label="Trend of the last stored runs">
              <line x1="0" y1={rowLine(row).limitY} x2="118" y2={rowLine(row).limitY} class="limit" />
              {#if rowLine(row).hasLine}<polyline points={rowLine(row).points} class="line" />{/if}
            </svg>
          {/if}
          <span class="times"><b>{duration(row.typicalMs)}</b>typical · last {duration(row.lastMs)}</span>
        </button>
      {/each}
    {/if}
  {/if}
{/if}
</div>

<style>
  .health { display: flex; flex-direction: column; min-width: 0; flex: 1; }
  .summary { margin: 0 0 var(--s3); color: var(--muted); font-size: var(--t-ui); }
  .summary span { margin-left: var(--s2); }
  /* The wrapper holds the room for the budget labels; the plot is exactly the
     drawn height, so a column's pixels and a line's pixels share one zero. */
  .chart { padding: 18px 2px 0; margin: 0 0 6px; }
  .plot { position: relative; display: flex; align-items: flex-end; justify-content: center; gap: 4px; }
  .col { flex: 1 1 0; min-width: 4px; max-width: 40px; display: flex; flex-direction: column-reverse; gap: 0; position: relative; padding: 0; border: 0; background: none; cursor: pointer; height: 100%; }
  .col:hover .band { filter: brightness(1.25); }
  .band { display: block; flex: none; width: 100%; border-radius: 3px 3px 0 0; }
  .queued { display: block; width: 100%; border-radius: 0 0 2px 2px; background: repeating-linear-gradient(45deg, var(--raise), var(--raise) 2px, transparent 2px, transparent 4px); }
  .waits { display: flex; align-items: flex-end; justify-content: center; gap: 4px; height: 12px; padding: 0 2px; margin: -6px 0 4px; }
  .waits .queued { max-width: 40px; flex: 1 1 0; }
  .queued.none { background: none; }
  .b0 { background: #4c4a52; } .b1 { background: #6c6a74; } .b2 { background: #8a8792; }
  .b3 { background: #a5a2ab; } .b4 { background: #585560; } .b5 { background: #787480; }
  .dot { position: absolute; left: 50%; bottom: -8px; width: 5px; height: 5px; margin-left: -2.5px; border-radius: 50%; background: var(--coral); }
  .warn, .fail { position: absolute; left: 0; right: 0; z-index: 2; pointer-events: none; border-top: 1px dashed var(--faint); }
  .fail { border-top-style: dotted; border-top-color: var(--muted); }
  .warn em, .fail em { position: absolute; left: 2px; top: -14px; font-style: normal; font-size: var(--t-xs); color: var(--faint); background: var(--main); padding: 0 4px; }
  .caption { margin: 4px 0 var(--s3); color: var(--faint); font-size: var(--t-sm); }
  .legend { display: flex; flex-wrap: wrap; gap: var(--s3); margin: 0 0 var(--s4); font-size: var(--t-sm); color: var(--faint); }
  .legend span { display: inline-flex; align-items: center; gap: 5px; }
  .legend i { width: 9px; height: 9px; border-radius: 2px; display: inline-block; }
  .legend .dotmark { border-radius: 50%; background: var(--coral); }
  .tests { margin: var(--s4) 0 var(--s2); font-size: var(--t-ui); color: var(--muted); font-weight: 600; }
  .row, .runrow { display: flex; flex-wrap: nowrap; gap: 10px; align-items: center; width: 100%; max-width: 100%; min-width: 0; text-align: left; padding: 7px 8px 7px 0; border: 0; border-bottom: 1px solid var(--line-soft); background: none; color: inherit; cursor: pointer; transition: background-color var(--motion-fast) var(--ease); }
  .row > .rule, .runrow > .rule { flex: none; width: 3px; align-self: stretch; }
  .row > .id, .runrow > .id { flex: 1 1 auto; min-width: 0; }
  .row > .spark { flex: none; width: 118px; }
  .row > .times, .runrow > .times { flex: none; width: 150px; }
  .runrow:hover, .row:hover { background: var(--hover); }
  .runrow .name { color: var(--ink); }
  .runrow .rule.over { background: var(--gold); }
  .row:last-child { border-bottom: 0; }

  .rule { border-radius: 2px; }
  .row.recurring .rule { background: var(--iris); }
  .row.new .rule { background: var(--gold); }
  .row.ignored .rule { background: repeating-linear-gradient(180deg, var(--faint), var(--faint) 3px, transparent 3px, transparent 6px); }
  .id { min-width: 0; }
  .name { display: block; font-family: var(--mono); font-size: var(--t-sm); color: var(--muted); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .row.recurring .name, .row.new .name { color: var(--ink); font-weight: 600; }
  .row.ignored .name { color: var(--faint); }
  .meta { display: block; margin-top: 2px; font-size: var(--t-xs); color: var(--faint); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .spark { width: 118px; height: 26px; }
  .limit { stroke: var(--coral); stroke-dasharray: 2 3; stroke-width: 1; }
  .line { fill: none; stroke: var(--ink-2); stroke-width: 1.6; }
  .row.ignored .line { stroke: var(--faint); }
  .times { font-size: var(--t-xs); color: var(--muted); text-align: right; }
  .times b { display: block; font-size: var(--t-sm); color: var(--ink); }
  .row.quiet .times b, .row.ignored .times b { color: var(--muted); font-weight: 500; }
  .level { padding: 0 var(--s1); }
  .back { margin: 0 0 var(--s2); padding: 0; border: 0; background: none; color: var(--muted); font-size: var(--t-sm); cursor: pointer; transition: color var(--motion-fast) var(--ease); }
  .back:hover { color: var(--ink); }
  .lhead h2 { margin: 0 0 4px; font-size: var(--t-title); display: flex; align-items: baseline; gap: var(--s2); flex-wrap: wrap; }
  .stateword { font-size: var(--t-sm); color: var(--muted); font-weight: 500; }
  .kv { margin: 0; font-size: var(--t-sm); color: var(--muted); display: flex; flex-wrap: wrap; gap: 5px; }
  .why { margin: 2px 0 0; font-size: var(--t-sm); color: var(--faint); font-style: italic; }
  h3 { margin: var(--s4) 0 var(--s2); font-size: var(--t-ui); color: var(--muted); font-weight: 600; }
  .bar { display: flex; align-items: center; gap: var(--s2); margin: 0 0 4px; }
  .bn { width: 120px; font-family: var(--mono); font-size: var(--t-sm); color: var(--ink-2); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .track { flex: 1; height: 8px; background: var(--rail); border-radius: 2px; overflow: hidden; }
  .fill { display: block; height: 100%; background: #6c6a74; border-radius: 2px; }
  .bt { width: 58px; text-align: right; font-size: var(--t-sm); color: var(--muted); }
  .steps { border-left: 1px solid var(--line-soft); margin: 0 0 var(--s2) 6px; padding: 2px 0 2px 8px; }
  .bar.step .bn { width: 104px; font-family: var(--sans); color: var(--muted); font-size: var(--t-xs); }
  .bar.step .fill { background: #4c4a52; }
  .wide { width: 100%; height: 96px; margin: var(--s2) 0 0; }
  .table { border-top: 1px solid var(--line-soft); }
  .trow { display: grid; grid-template-columns: 90px 90px 1fr; gap: var(--s2); width: 100%; text-align: left; padding: 5px 0; border: 0; border-bottom: 1px solid var(--line-soft); background: none; color: var(--ink-2); font-size: var(--t-sm); cursor: pointer; transition: background-color var(--motion-fast) var(--ease); }
  .trow .dur { color: var(--ink); }
  .trow:hover { background: var(--hover); }
  .when { color: var(--muted); }
  .mono { font-family: var(--mono); font-size: var(--t-xs); }
  .acts { display: flex; flex-wrap: wrap; align-items: center; gap: var(--s2); margin: var(--s3) 0 0; font-size: var(--t-sm); color: var(--faint); }
  .link { border: 0; background: none; padding: 0; color: var(--iris); font-size: var(--t-sm); cursor: pointer; }
  .link:hover { text-decoration: underline; }
  .allow { margin: var(--s3) 0 0; padding: var(--s3); background: var(--side); border: 1px solid var(--line); border-radius: 10px; max-width: 620px; }
  .allow input { width: 100%; margin: 0 0 var(--s2); padding: 7px 9px; background: var(--field); border: 1px solid var(--line); border-radius: 7px; color: var(--ink); font: inherit; font-size: var(--t-sm); }
  .primary { padding: 6px 12px; border-radius: 7px; border: 1px solid var(--line); background: var(--raise); color: var(--ink); font-size: var(--t-sm); font-weight: 600; cursor: pointer; }
  .primary:disabled { opacity: 0.6; cursor: default; }
  .row:active, .runrow:active, .trow:active { background: var(--select); }
  .back:active { color: var(--ink-2); }
  .err { margin: 4px 0; color: var(--coral); font-size: var(--t-sm); }
  .loading { min-height: 120px; }
  @media (max-width: 780px) {
    .row, .runrow { flex-wrap: wrap; }
    .row > .id, .runrow > .id { flex: 1 1 calc(100% - 13px); }
    .row > .times, .runrow > .times { flex: 1 1 100%; width: auto; text-align: left; margin: -2px 0 0 13px; }
    .times b { display: inline; margin-right: 6px; }
    .bn { width: 84px; }
    .bar.step .bn { width: 72px; }
    .bt { width: 48px; }
    .trow { grid-template-columns: 80px 80px; }
    .trow .when { grid-column: 1 / -1; }
  }
</style>
