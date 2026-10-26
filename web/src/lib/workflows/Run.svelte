<script lang="ts">
  // One workflow run: a summary with progress, the phases as a stepper, the
  // agents grouped by phase, the log, and the result. Bounds come from
  // roomsvc.RunDetail. A running run can be paused, resumed or canceled;
  // onpause(run) toggles the pause and resolves when the engine has it.
  import { ago, duration, elapsed, isLongCall } from '../ui/format.ts'
  import { api, copyText } from '../../api.ts'
  import { createResultLoader, createWorkerResults, type LoadedResult, type WorkerRows } from './runResult.ts'
  import { errorText } from '../errorText.ts'
  import type { RunAgent, RunDetail, RunSummary } from '../api/workflow.ts'
  import JsonView from '../ui/JsonView.svelte'
  import WorkerDetail from './WorkerDetail.svelte'
  import { previewLine } from './runPreview.ts'
  import Segmented from '../ui/Segmented.svelte'
  import RunIcon from './RunIcon.svelte'
  import RunGrid from './RunGrid.svelte'
  import PhaseList from './PhaseList.svelte'
  import { cellOf, laneGrid, type CellRef, type CellState } from './lanes.ts'

  interface Props {
    detail?: RunDetail | null
    error?: string
    oncancel?: ((run: RunSummary) => void) | null
    onpause?: ((run: RunSummary) => Promise<void>) | null
    onopen?: ((run: RunSummary, agent: RunAgent) => void) | null
  }
  interface Group {
    phase: string
    lane?: string
    stage?: string
    agents: RunAgent[]
    state: CellState
    done: number
  }
  // One thing the owner should look at: a failed agent, or the longest worker.
  interface Attention {
    key: string
    text: string
    failed: boolean
    group: number
  }
  let { detail = null, error = '', oncancel = null, onpause = null, onopen = null }: Props = $props()

  const label: Record<string, string> = { running: 'Running', ok: 'Done', error: 'Failed', canceled: 'Canceled', interrupted: 'Interrupted', unknown: 'No end recorded' }

  let pausing = $state(false)
  let pauseError = $state('')
  async function togglePause() {
    const target = detail?.run
    if (!target || !onpause) return
    pausing = true
    pauseError = ''
    try {
      await onpause(target)
    } catch (e) {
      pauseError = errorText(e)
    } finally {
      pausing = false
    }
  }
  // Agents keep run order; a group starts where the phase changes.
  let groups = $derived.by(() => {
    const phases: { phase: string; lane?: string; stage?: string; agents: RunAgent[] }[] = []
    for (const a of detail?.agents ?? []) {
      const phase = a.phase || 'Agents'
      const last = phases[phases.length - 1]
      if (last?.phase === phase && last.lane === a.lane && last.stage === a.stage) last.agents.push(a)
      else phases.push({ phase, lane: a.lane, stage: a.stage, agents: [a] })
    }
    return phases.map((g): Group => {
      const st = g.agents.map((a) => a.status)
      return {
        ...g,
        state: st.includes('running') ? 'running' : st.some((s) => s === 'error' || s === 'failed') ? 'error' : 'ok',
        done: st.filter((s) => s !== 'running').length,
      }
    })
  })
  // Phases that share stages show as a lanes grid; the chosen cell opens its agents.
  let grid = $derived(laneGrid(groups.map((g) => ({ phase: g.phase, lane: g.lane, stage: g.stage, state: g.state, done: g.done, total: g.agents.length }))))
  let picked = $state<CellRef | null>(null)
  // A new run starts with no cell open. Only the id is read, so the refresh that
  // replaces `detail` every 2 seconds keeps the open cell.
  let runId = $derived(detail?.run.id ?? '')
  // Without a grid, each phase row opens on its own.
  let openPhases = $state<number[]>([])
  $effect(() => {
    void runId
    picked = null
    openPhases = []
  })
  const togglePhase = (i: number) => (openPhases = openPhases.includes(i) ? openPhases.filter((n) => n !== i) : [...openPhases, i])
  let shownGroups = $derived.by(() => {
    if (!grid) return groups.length > 1 ? [] : groups
    const cell = picked ? grid.lanes.find((l) => l.name === picked?.lane)?.cells[picked.col] : null
    return (cell?.phases ?? []).map((i) => groups[i]).filter((g): g is Group => !!g)
  })
  const maxAttention = 3
  let attention = $derived.by(() => {
    const out: Attention[] = []
    groups.forEach((g, group) => {
      for (const a of g.agents) if (a.status === 'error' || a.status === 'failed') out.push({ key: `f${a.id}`, text: `${a.label || `agent ${a.id}`} failed in ${g.phase}`, failed: true, group })
    })
    const items = out.slice(0, maxAttention)
    const running = groups.flatMap((g, group) => g.agents.filter((a) => a.status === 'running' && a.started).map((a) => ({ a, group, since: a.started ?? now })))
    const slow = running.reduce<(typeof running)[number] | null>((best, r) => (!best || r.since < best.since ? r : best), null)
    if (slow) items.push({ key: `r${slow.a.id}`, text: `${slow.a.label || `agent ${slow.a.id}`} has run ${elapsed(now - slow.since)} in ${groups[slow.group]?.phase}`, failed: false, group: slow.group })
    return { items, more: Math.max(0, out.length - maxAttention) }
  })
  function pickGroup(group: number) {
    if (grid) picked = cellOf(grid, group)
  }
  // Counts come from Go over every agent, not just the shown ones.
  let counts = $derived({ running: detail?.agentRunning ?? 0, error: detail?.agentFailed ?? 0, total: detail?.agentTotal ?? 0 })
  let pct = $derived(counts.total ? Math.round(((counts.total - counts.running) / counts.total) * 100) : 0)
  // The result loads on its own, in full; run details carry only its size.
  let full = $state<LoadedResult>(null)
  const loadResult = createResultLoader(api, (v) => (full = v))
  $effect(() => loadResult(detail))
  // A JSON result reads as fields; Raw shows the exact text.
  const parse = (text: string): unknown => { try { return JSON.parse(text) } catch { return text } }
  let loaded = $derived(full && 'result' in full ? full : null)
  let loadError = $derived(full && 'error' in full ? full.error : '')
  let resultValue = $derived(loaded?.result ? parse(loaded.result) : null)
  let resultMode = $state('readable')
  // A running run ticks its workers' elapsed time each second.
  let now = $state(Date.now())
  $effect(() => {
    if (detail?.run.status !== 'running') return
    const t = setInterval(() => (now = Date.now()), 1000)
    return () => clearInterval(t)
  })
  // Worker rows open in place and load the worker's full result once.
  let workerRows = $state<WorkerRows>({})
  const workers = createWorkerResults(api, (v) => (workerRows = v))
  $effect(() => workers.sync(detail?.run.id ?? ''))
  // Each run detail update refreshes open rows of running workers.
  $effect(() => workers.refresh(detail?.run.id ?? '', detail?.agents))
  let copied = $state(false)
  async function copy() {
    if (!loaded) return
    await copyText(loaded.result)
    copied = true
    setTimeout(() => (copied = false), 1500)
  }
  const secs = (ms: number) => (ms >= 60000 ? `${Math.floor(ms / 60000)}m ${String(Math.round((ms % 60000) / 1000)).padStart(2, '0')}s` : `${(ms / 1000).toFixed(ms < 10000 ? 1 : 0)}s`)
  const clockOf = (ts: number) => new Date(ts).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' })
</script>

<section class="run">
  {#if error}
    <p class="err">{error}</p>
  {:else if !detail}
    <p class="quiet">Loading run…</p>
  {:else}
    {@const run = detail.run}
    {#snippet agentRows(g: Group)}
      <div class="agents">
        {#each g.agents as a (a.id)}
          {@const row = workerRows[a.id]}
          <div class="agent {a.status}" class:expanded={row?.open}>
            <RunIcon status={a.status} size={18} />
            <span class="who">
              <span class="label">{a.label || `agent ${a.id}`}</span>
              {#if a.profile}<span class="pill">{a.profile}</span>{/if}
              {#if a.cached}<span class="pill">cached</span>{/if}
            </span>
            <span class="time">
              {#if a.session && onopen}<button class="session" onclick={() => onopen?.(run, a)} title="Open this agent's session to read it or send it a message">Session</button>{/if}
              {a.durMs ? secs(a.durMs) : a.status === 'running' ? (a.started ? secs(now - a.started) : 'working…') : '—'}
              {#if a.status === 'ok' || a.status === 'running'}<button class="open" aria-expanded={!!row?.open} aria-label="{row?.open ? 'Hide' : 'Show'} the {a.status === 'running' ? 'journal' : 'result'} of {a.label || `agent ${a.id}`}" onclick={() => workers.toggle(run.id, a.id, a.status)}><span aria-hidden="true">›</span></button>{/if}
            </span>
            {#if a.error}
              <p class="line bad">{a.error}</p>
            {:else if a.status === 'running'}
              {@const ms = a.doing?.since ? now - a.doing.since : 0}
              <p class="line live" class:long={isLongCall(ms)}>{a.doing?.text || 'Starting'}{#if ms}{' · ' + elapsed(ms)}{/if}</p>
            {:else if a.preview}
              {@const line = previewLine(a.preview)}
              {#if line}<p class="line">{line}</p>{/if}
            {/if}
            {#if row?.open}
              <div class="full">
                {#if row.value}<WorkerDetail value={row.value} />{/if}
                {#if row.loading}<p class="quiet" role="status">{row.value ? 'Refreshing the result…' : 'Loading the result…'}</p>{/if}
                {#if row.error}<div class="retry-row"><p class="err">Could not load the result: {row.error}</p><button class="retry" aria-label="Retry result for {a.label || ('agent ' + a.id)}" onclick={() => workers.retry(run.id, a.id, a.status)}>Retry</button></div>{/if}
              </div>
            {/if}
          </div>
        {/each}
      </div>
    {/snippet}
    <header class="hero {run.status}">
      <div class="title">
        <RunIcon status={run.status} size={30} />
        <div class="tt">
          <h1>{run.name}</h1>
          <p class="mono">{run.id}{#if run.parent}<span class="sans">&nbsp;· started by {run.parent}</span>{/if}</p>
        </div>
        <span class="chip {run.status}" class:paused={run.paused}>{run.paused ? 'Paused' : label[run.status] ?? run.status}</span>
        {#if run.status === 'running' && onpause}
          <button class="pause" disabled={pausing} onclick={togglePause}>{run.paused ? 'Resume' : 'Pause'}</button>
        {/if}
        {#if run.status === 'running' && oncancel}
          <button class="cancel" onclick={() => oncancel?.(run)}>Cancel run</button>
        {/if}
      </div>
      {#if counts.total}
        <div class="bar stacked" role="img" aria-label="{counts.total - counts.running - counts.error} done, {counts.running} running, {counts.error} failed of {counts.total} agents">
          <span class="seg ok" style="width:{((counts.total - counts.running - counts.error) / counts.total) * 100}%"></span>
          <span class="seg error" style="width:{(counts.error / counts.total) * 100}%"></span>
          <span class="seg running" style="width:{(counts.running / counts.total) * 100}%"></span>
        </div>
      {:else}
        <div class="bar"><span class="fill {run.status}" style="width:{run.status === 'running' ? pct : 100}%"></span></div>
      {/if}
      <dl>
        <div><dt>Started</dt><dd>{ago(run.startedAt)}</dd></div>
        <div><dt>Duration</dt><dd>{duration(run.startedAt, run.status === 'running' ? null : run.endedAt)}</dd></div>
        <div><dt>Agents</dt><dd>{counts.total}</dd></div>
        <div><dt>Working</dt><dd class:gold={counts.running}>{counts.running}</dd></div>
        <div><dt>Failed</dt><dd class:bad={counts.error}>{counts.error}</dd></div>
      </dl>
      {#if pauseError}
        <p class="runerr" role="alert">{pauseError}</p>
      {/if}
      {#if run.paused}
        <p class="runnote">This run is paused. Agents that are running finish, and no new agents start until you resume it.</p>
      {/if}
      {#if run.status === 'canceled'}
        <p class="runnote">This run was canceled. Results from agents that finished are shown below.</p>
      {:else if run.error}
        <p class="runerr">{run.error}</p>
      {/if}
    </header>

    {#if grid}
      {#if attention.items.length}
        <div class="attention" role="group" aria-label="Needs attention">
          {#each attention.items as item (item.key)}
            <button class="att" class:failed={item.failed} onclick={() => pickGroup(item.group)}>{item.text}</button>
          {/each}
          {#if attention.more}<span class="quiet">and {attention.more} more failed</span>{/if}
        </div>
      {/if}
      <RunGrid {grid} {picked} onpick={(ref) => (picked = ref)} />
      {#if !picked}<p class="quiet">Tap a cell to see its agents.</p>{/if}
    {:else if groups.length > 1}
      <PhaseList rows={groups.map((g) => ({ phase: g.phase, state: g.state, done: g.done, total: g.agents.length }))} open={openPhases} ontoggle={togglePhase}>
        {#snippet agents(i)}{#if groups[i]}{@render agentRows(groups[i])}{/if}{/snippet}
      </PhaseList>
    {/if}

    {#if !detail.agents.length}
      <p class="quiet">{run.status === 'running' ? 'No agent has started yet.' : 'No agents ran in this workflow.'}</p>
    {/if}
    {#if detail.agentTotal > detail.agents.length}
      <p class="quiet">Showing the last {detail.agents.length} of {detail.agentTotal} agents.</p>
    {/if}
    {#each shownGroups as g}
      <h2>{g.phase}<span>{g.agents.length} agent{g.agents.length === 1 ? '' : 's'}</span></h2>
      {@render agentRows(g)}
    {/each}

    {#if detail.logs.length}
      <h2>Log<span>{detail.logs.length} line{detail.logs.length === 1 ? '' : 's'}</span></h2>
      <div class="logs mono">
        {#each detail.logs as l}
          <div><time>{clockOf(l.ts)}</time><span>{l.msg}</span></div>
        {/each}
      </div>
    {/if}

    {#if detail.resultBytes}
      <div class="rhead"><h2>Result</h2>{#if loaded?.result}<Segmented label="Result view" options={[['readable', 'Readable'], ['raw', 'Raw JSON']]} value={resultMode} onchange={(v: string) => (resultMode = v)} /><button class="copy" onclick={copy}>{copied ? 'Copied' : 'Copy JSON'}</button>{/if}</div>
      {#if !full}<p class="quiet" role="status">Loading the result…</p>
      {:else if loadError}<div class="retry-row"><p class="err">Could not load the result: {loadError}</p><button class="retry" aria-label="Retry run result" onclick={() => loadResult.retry(detail)}>Retry</button></div>
      {:else if loaded?.result}
      {#if resultMode === 'raw'}<pre class="raw mono">{loaded.result}</pre>{:else}<div class="result"><JsonView value={resultValue} /></div>{/if}
      {#if loaded.truncatedKB}<p class="quiet">The result is truncated at {loaded.truncatedKB} KB; this shows its start. <span class="mono">dyna runs show {run.id}</span> prints all of it.</p>{/if}
      {/if}
    {/if}
  {/if}
</section>

<style>
  .retry-row { display: flex; flex-wrap: wrap; align-items: center; gap: var(--s2); }
  .retry-row .err { overflow-wrap: anywhere; }
  .retry { min-height: 44px; padding: 8px 14px; border-radius: var(--r-xs); background: var(--raise); color: var(--ink); flex: none; }
  .line.live.long { font-weight: 600; }
  .line.live { color: var(--gold); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .open { margin-left: 8px; color: var(--faint); width: 22px; height: 22px; border-radius: 5px; display: inline-grid; place-items: center; transition: transform var(--motion-fast) var(--ease); }
  .agent.expanded .open { transform: rotate(90deg); }
  .full { grid-column: 2 / 4; margin-top: 10px; min-width: 0; overflow-wrap: anywhere; }
  .rhead { display: flex; align-items: center; gap: 10px; margin: 22px 0 8px; }
  .rhead h2 { margin: 0; flex: 1; }
  .raw { white-space: pre-wrap; overflow-wrap: anywhere; background: #19181b; border: 1px solid var(--line-soft); border-radius: 10px; padding: 12px 16px; font-size: 12.5px; max-height: 70vh; overflow: auto; margin: 0; }
  .run { flex: 1; overflow-y: auto; padding: 20px 20px 32px; min-height: 0; min-width: 0; }
  .quiet { color: var(--faint); font-size: 13.5px; }
  .err { color: var(--coral); }
  .gold { color: var(--gold); }
  .bad { color: var(--coral); }
  .hero { background: var(--side); border: 1px solid var(--line-soft); border-radius: 14px; padding: 18px 20px 16px; }
  .hero.running { border-color: color-mix(in srgb, var(--gold) 28%, var(--line-soft)); }
  .hero.error, .hero.interrupted { border-color: color-mix(in srgb, var(--coral) 28%, var(--line-soft)); }
  .title { display: flex; align-items: center; gap: 14px; }
  .tt { flex: 1; min-width: 0; }
  h1 { margin: 0; font-size: 22px; font-weight: 700; }
  .tt p { margin: 2px 0 0; font-size: 12px; color: var(--faint); }
  .sans { font-family: var(--sans); font-size: 12.5px; }
  .chip { font-size: 12px; font-weight: 600; padding: 3px 10px; border-radius: 12px; background: var(--raise); color: var(--muted); }
  .chip.running { color: var(--gold); background: rgba(230, 192, 123, 0.12); }
  .chip.ok { color: var(--sage); background: rgba(151, 201, 149, 0.12); }
  .chip.error, .chip.interrupted { color: var(--coral); background: rgba(239, 138, 128, 0.12); }
  .cancel { font-size: 13px; font-weight: 600; padding: 6px 12px; border-radius: 7px; color: var(--coral); background: rgba(239, 138, 128, 0.1); border: 1px solid rgba(239, 138, 128, 0.28); flex: none; transition: background-color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .cancel:hover { background: color-mix(in srgb, var(--coral) 16%, transparent); }
  }
  .cancel:active { background: var(--select); }
  .chip.paused { color: var(--muted); background: var(--raise); }
  .pause { font-size: 13px; font-weight: 600; padding: 6px 12px; border-radius: 7px; color: var(--ink-2); background: var(--raise); border: 1px solid var(--line-soft); flex: none; }
  .pause:disabled { color: var(--muted); }
  .pause:not(:disabled) { transition: background-color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .pause:hover:not(:disabled) { background: var(--raise-hover); }
  }
  .pause:active:not(:disabled) { background: var(--select); }
  .session { font-size: 12px; font-weight: 600; color: var(--ink-2); background: var(--raise); border-radius: 6px; padding: 3px 10px; margin-right: 10px; transition: color var(--motion-fast) var(--ease), background-color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .session:hover { color: var(--ink); background: var(--raise-hover); }
  }
  .session:active { background: var(--select); }
  .bar { height: 6px; border-radius: 3px; background: var(--rail); overflow: hidden; margin: 16px 0 14px; }
  .stacked { display: flex; }
  .seg { display: block; height: 100%; transition: width var(--motion-slow); }
  .seg.ok { background: var(--sage); }
  .seg.error { background: var(--coral); }
  .seg.running { background: var(--gold); }
  .attention { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; margin: 14px 0 0; }
  .att { font-size: 13px; color: var(--gold); background: var(--gold-wash); border: 1px solid color-mix(in srgb, var(--gold) 28%, transparent); border-radius: 8px; padding: 6px 10px; text-align: left; overflow-wrap: anywhere; }
  .att.failed { color: var(--coral); background: color-mix(in srgb, var(--coral) 8%, transparent); border-color: color-mix(in srgb, var(--coral) 30%, transparent); }
  @media (max-width: 760px) { .att { min-height: 44px; width: 100%; } }
  .fill { display: block; height: 100%; border-radius: 3px; background: var(--faint); transition: width var(--motion-slow); }
  .fill.running { background: linear-gradient(90deg, #b59654, var(--gold)); }
  .fill.ok { background: linear-gradient(90deg, #6fa66d, var(--sage)); }
  .fill.error, .fill.interrupted { background: linear-gradient(90deg, #b8574d, var(--coral)); }
  dl { display: grid; grid-template-columns: repeat(5, minmax(0, 1fr)); gap: 8px; margin: 0; }
  dt { font-size: 11px; font-weight: 700; color: var(--faint); text-transform: uppercase; letter-spacing: 0.06em; }
  dd { margin: 3px 0 0; font-size: 16px; font-weight: 600; color: var(--ink); font-variant-numeric: tabular-nums; }
  .runnote { color: var(--muted); margin: 14px 0 0; font-size: 14px; background: var(--raise); border-radius: 8px; padding: 10px 12px; }
  .runerr { color: var(--coral); margin: 14px 0 0; font-size: 14px; background: rgba(239, 138, 128, 0.08); border-radius: 8px; padding: 10px 12px; }
  h2 { display: flex; align-items: center; gap: 8px; font-size: 12px; font-weight: 700; color: var(--muted); text-transform: uppercase; letter-spacing: 0.06em; margin: 24px 0 8px; }
  h2 span { text-transform: none; letter-spacing: 0; font-weight: 500; color: var(--faint); }
  /* A group never clips or shrinks a row, so every row keeps its whole content:
     the corners are rounded on the group and on its first and last rows. */
  .agents { background: var(--side); border: 1px solid var(--line-soft); border-radius: 12px; }
  .agent { display: grid; grid-template-columns: 18px minmax(0, 1fr) auto; align-items: center; column-gap: 12px; padding: 11px 14px; min-width: 0; }
  .agent:first-child { border-radius: 11px 11px 0 0; }
  .agent:last-child { border-radius: 0 0 11px 11px; }
  .agent:only-child { border-radius: 11px; }
  .agent + .agent { border-top: 1px solid var(--line-soft); }
  .agent.running { background: rgba(230, 192, 123, 0.035); }
  .who { display: flex; align-items: center; gap: 8px; min-width: 0; }
  .label { font-weight: 600; font-size: 14px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .pill { font-size: 11.5px; color: var(--muted); background: var(--raise); border-radius: 10px; padding: 1px 8px; }
  .time { font-size: 12.5px; color: var(--muted); font-variant-numeric: tabular-nums; display: flex; align-items: center; }
  .agent.running .time { color: var(--gold); }
  .line { grid-column: 2 / 4; margin: 4px 0 0; font-size: 13px; color: var(--muted); white-space: pre-wrap; word-break: break-word; line-height: 1.45; }
  .line.bad { color: var(--coral); }
  .logs { background: #19181b; border: 1px solid var(--line-soft); border-radius: 10px; padding: 10px 14px; font-size: 12.5px; color: var(--ink-2); max-height: 260px; overflow-y: auto; }
  .logs div { display: flex; gap: 14px; padding: 2px 0; }
  .logs time { color: var(--faint); flex: none; }
  .copy { margin-left: auto; text-transform: none; letter-spacing: 0; font-size: 12px; font-weight: 600; color: var(--muted); padding: 3px 10px; border-radius: 6px; background: var(--raise); transition: color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .copy:hover { color: var(--ink); }
  }
  .result { background: #19181b; border: 1px solid var(--line-soft); border-radius: 10px; padding: 12px 16px; font-size: 14px; }
  @media (max-width: 760px) {
    .run { padding: 12px 10px 24px; }
    .title { flex-wrap: wrap; }
    .cancel, .pause { min-height: 44px; width: 100%; order: 5; }
    dl { grid-template-columns: repeat(3, minmax(0, 1fr)); row-gap: 12px; }
    .session { min-height: 36px; padding: 0 12px; margin-right: 6px; }
    .time { white-space: nowrap; }
    .line.live { white-space: normal; overflow: visible; text-overflow: clip; overflow-wrap: anywhere; }
  }
</style>
