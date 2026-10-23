<script lang="ts">
  // CI health, the CI tab of the Services page: one status line, a chart of
  // each run's time against the budget, the slowest jobs, then the slow tests.
  // A bar takes its colour from the budget state alone. A test row's state is
  // a rule at the edge, a type weight and a line of words, never a chip.
  import Segmented from '../ui/Segmented.svelte'
  import EmptyState from '../ui/EmptyState.svelte'
  import { api } from '../../api.ts'
  import {
    barPct,
    breachLimit,
    budgetState,
    budgetWord,
    budgetWithAllow,
    capRows,
    duration,
    filterTests,
    graphRuns,
    graphScale,
    jobRanking,
    readCIHealth,
    readTestDetail,
    rowLimit,
    slowestJobs,
    spark,
    statusLine,
    stateLine,
    type CIHealth,
    type CIHealthTest,
    type TestFilter,
  } from './health.ts'

  interface Props {
    roomId: string
    changes?: unknown
    onpr?: (pr: number) => void
  }
  let { roomId, changes = null, onpr = () => {} }: Props = $props()

  // The client's call names, typed so the readers get unknown data.
  type Calls = Pick<typeof api, 'ciHealth' | 'ciHealthTest' | 'roomCIBudget' | 'setRoomCIBudget'>
  const calls: Calls = api

  let health = $state<CIHealth | null>(null)
  let error = $state('')
  let note = $state('')
  let test = $state<CIHealthTest | null>(null)
  let filter = $state<TestFilter>('all')
  let picked = $state('')
  let hovered = $state('')
  let allowOpen = $state(false)
  let allowReason = $state('')
  let allowError = $state('')
  let allowing = $state(false)
  let seq = 0

  const runs = $derived(health?.runs ?? [])
  const scale = $derived(graphScale(runs, health?.failMs ?? 0))
  // Newest first from the store; the chart draws oldest on the left.
  const graph = $derived(graphRuns(runs, 0))
  const active = $derived(runs.find((item) => item.token === (hovered || picked)) ?? graph.newest)
  const jobs = $derived(jobRanking(runs))
  const tests = $derived(health ? filterTests(health.tests, filter) : [])
  const capped = $derived(capRows(tests, rowLimit))
  const breaches = $derived(test ? capRows(test.runs.filter((row) => row.over), breachLimit) : { shown: [], hidden: 0 })
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

  async function openTest(name: string): Promise<void> {
    note = ''
    allowOpen = false
    try {
      const detail = readTestDetail(await calls.ciHealthTest(roomId, name))
      if (detail) test = detail.test
      else note = 'That test could not be read.'
    } catch (e) {
      note = message(e)
    }
  }

  function back(): void {
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

  const trendText = (rank: { trend: string; changePct: number }): string =>
    rank.trend === 'steady' ? 'steady' : `${rank.trend} ${rank.changePct > 0 ? '+' : ''}${rank.changePct}%`

  const stateOf = (row: CIHealthTest): string => (row.state === 'ignored' ? 'allowed' : row.state)
  // The two charts keep their own scale and size; the helpers keep the markup
  // to one expression per attribute.
  const testLine = (row: CIHealthTest) => spark(row.runs, 520, 96, row.limitMs)
  const rowLine = (row: CIHealthTest) => spark(row.runs, 118, 26, row.limitMs)
</script>

<div class="health">
{#if note}<p class="err" role="alert">{note}</p>{/if}

{#if test}
  <section class="level" aria-label="Test {test.name}">
    <button class="back" onclick={back}>‹ CI health</button>
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
          <button class="trow" onclick={() => onpr(row.pr)} aria-label="Open PR #{row.pr}, run {runLabel(row.token)}">
            <span class="mono">#{row.pr}</span>
            <span class="dur">{duration(row.durationMs)}</span>
            <span class="when">{row.at.replace('T', ' ').slice(0, 16)}</span>
          </button>
        {/each}
      </div>
    {/if}
    <div class="acts">
      {#if test.state !== 'ignored'}<button class="link" onclick={() => { allowOpen = !allowOpen; allowError = '' }}>{allowOpen ? 'Cancel' : 'Allow…'}</button>{/if}
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
  {:else if runs.length === 0 && health.tests.length === 0}
    <EmptyState title="No CI run yet">A run is recorded when the watcher inspects one of this room's pull requests.</EmptyState>
  {:else}
    {#if active}
      <p class="status">{statusLine(graph.newest)}</p>

      <div class="plot" role="group" aria-label="Time of each run against the budget, oldest on the left">
        {#if health.warnMs > 0}<div class="line-warn" style:bottom="{barPct(health.warnMs, scale)}%"></div>{/if}
        {#if health.failMs > 0}<div class="line-fail" style:bottom="{barPct(health.failMs, scale)}%"></div>{/if}
        {#each graph.shown as item (item.token)}
          {@const state = budgetState(item.busyMs, item.warnMs, item.failMs)}
          <button
            class="col {state}"
            class:on={item.token === active.token}
            aria-pressed={item.token === active.token}
            aria-label="Run {runLabel(item.token)}: {duration(item.busyMs)}, {budgetWord(state)}"
            onclick={() => (picked = item.token)}
            onmouseenter={() => (hovered = item.token)}
            onmouseleave={() => (hovered = '')}
          >
            <span class="bar" style:height="{Math.max(barPct(item.busyMs, scale), 2)}%"></span>
          </button>
        {/each}
      </div>
      <p class="pick" aria-live="polite">
        <b>{runLabel(active.token)}</b>
        <span>{duration(active.busyMs)} · {budgetWord(budgetState(active.busyMs, active.warnMs, active.failMs))}</span>
        {#each slowestJobs(active) as job (job.id)}<span class="job">{job.name} {duration(job.busyMs)}</span>{/each}
        {#if active.pr}<button class="link" onclick={() => onpr(active.pr)}>PR #{active.pr}</button>{/if}
      </p>
    {/if}

    {#if jobs.length > 0}
      <h3>Slowest jobs</h3>
      <ol class="jobs">
        {#each jobs as rank (rank.name)}
          <li>
            <span class="jn">{rank.name}</span>
            <span class="jt">{duration(rank.typicalMs)}</span>
            <span class="trend {rank.trend}">{trendText(rank)}</span>
          </li>
        {/each}
      </ol>
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
          <svg class="spark" viewBox="0 0 118 26" preserveAspectRatio="none" role="img" aria-label="Trend of the last stored runs">
            <line x1="0" y1={rowLine(row).limitY} x2="118" y2={rowLine(row).limitY} class="limit" />
            {#if rowLine(row).hasLine}<polyline points={rowLine(row).points} class="line" />{/if}
          </svg>
          <span class="times"><b>{duration(row.typicalMs)}</b>typical · last {duration(row.lastMs)}</span>
        </button>
      {/each}
    {/if}
  {/if}
{/if}
</div>

<style>
  .health { display: flex; flex-direction: column; min-width: 0; flex: 1; container-type: inline-size; }
  .status { margin: 0 0 var(--s3); color: var(--ink); font-size: var(--t-ui); }
  /* A bar's height is a share of the plot, and a line's position is the same
     share, so both use one zero. */
  .plot { position: relative; display: flex; align-items: stretch; gap: 2px; height: 120px; }
  .col { flex: 1 1 0; min-width: 3px; max-width: 28px; display: flex; align-items: flex-end; padding: 0; border: 0; background: none; cursor: pointer; }
  .bar { display: block; width: 100%; border-radius: 2px 2px 0 0; background: var(--sage); opacity: 0.55; }
  .col.warn .bar { background: var(--gold); }
  .col.over .bar { background: var(--coral); }
  .col.on .bar { opacity: 1; }
  .line-warn, .line-fail { position: absolute; left: 0; right: 0; z-index: 1; pointer-events: none; border-top: 1px dashed var(--faint); }
  .line-warn { border-top-style: dotted; }
  .pick { display: flex; flex-wrap: wrap; align-items: baseline; gap: 2px var(--s3); margin: var(--s2) 0 0; font-size: var(--t-sm); color: var(--muted); min-height: 2.6em; }
  .pick b { color: var(--ink); font-family: var(--mono); font-size: var(--t-xs); }
  .job { color: var(--ink-2); }
  .jobs { list-style: none; margin: 0; padding: 0; }
  .jobs li { display: grid; grid-template-columns: 1fr auto 96px; gap: var(--s3); align-items: baseline; padding: 6px 0; border-bottom: 1px solid var(--line-soft); font-size: var(--t-sm); }
  .jobs li:last-child { border-bottom: 0; }
  .jn { font-family: var(--mono); color: var(--ink-2); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .jt { color: var(--ink); }
  .trend { text-align: right; color: var(--faint); font-size: var(--t-xs); }
  .trend.slower { color: var(--gold); }
  .caption { margin: 4px 0 var(--s3); color: var(--faint); font-size: var(--t-sm); }
  .tests { margin: var(--s4) 0 var(--s2); }
  .row { display: flex; flex-wrap: nowrap; gap: 10px; align-items: center; width: 100%; max-width: 100%; min-width: 0; text-align: left; padding: 7px 8px 7px 0; border: 0; border-bottom: 1px solid var(--line-soft); background: none; color: inherit; cursor: pointer; transition: background-color var(--motion-fast) var(--ease); }
  .row > .rule { flex: none; width: 3px; align-self: stretch; }
  .row > .id { flex: 1 1 auto; min-width: 0; }
  .row > .spark { flex: none; width: 118px; }
  .row > .times { flex: none; width: 150px; }
  .row:hover { background: var(--hover); }
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
  .row:active, .trow:active { background: var(--select); }
  .back:active { color: var(--ink-2); }
  .err { margin: 4px 0; color: var(--coral); font-size: var(--t-sm); }
  .loading { min-height: 120px; }
  @container (max-width: 560px) {
    .row { flex-wrap: wrap; }
    .row > .id { flex: 1 1 calc(100% - 13px); }
    .row > .spark { display: none; }
    .row > .times { flex: 1 1 100%; width: auto; text-align: left; margin: -2px 0 0 13px; }
    .times b { display: inline; margin-right: 6px; }
    .trow { grid-template-columns: 80px 80px; }
    .trow .when { grid-column: 1 / -1; }
    .jobs li { grid-template-columns: 1fr auto 84px; }
  }
</style>
