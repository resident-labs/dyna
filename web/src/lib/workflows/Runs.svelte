<script lang="ts">
  // A room's workflow runs: running runs as cards with live progress on top,
  // finished runs as a quiet list under them. Read-only; a click opens a run.
  import { ago, duration, roomLabel } from '../ui/format.js'
  import type { Room } from '../api/room.ts'
  import type { RunList, RunSummary } from '../api/workflow.ts'
  import RunIcon from './RunIcon.svelte'
  import TabHeader from '../rooms/TabHeader.svelte'
  import EmptyState from '../ui/EmptyState.svelte'

  interface Props {
    room: Room
    list?: RunList | null
    error?: string
    onopen: (run: RunSummary) => void
    onstart?: (() => void) | null
  }
  let { room, list = null, error = '', onopen, onstart = null }: Props = $props()

  const label: Record<string, string> = { running: 'Running', ok: 'Done', error: 'Failed', canceled: 'Canceled' }
  let running = $derived((list?.runs ?? []).filter((r) => r.status === 'running'))
  let recent = $derived((list?.runs ?? []).filter((r) => r.status !== 'running'))
  let usage = $derived(list?.runs?.length ? `${list.running} running · ${list.total} total` : '')
  const pct = (r: RunSummary) => (r.agents ? Math.round(((r.done + r.failed) / r.agents) * 100) : 0)
</script>

<section class="runs">
  <TabHeader title="Workflows" blurb="Runs started in #{roomLabel(room)} by you or its agents. They update live." {usage}>
    {#snippet action()}
      {#if onstart}
        <button class="start" onclick={onstart}>
          <svg viewBox="0 0 20 20" width="16" height="16" aria-hidden="true"><path d="M6.5 4.5v11l9-5.5Z" fill="currentColor" /></svg>
          Run workflow
        </button>
      {/if}
    {/snippet}
  </TabHeader>
  <div class="body">
  {#if error}
    <p class="err">{error}</p>
  {:else if !list}
    <p class="quiet">Loading runs…</p>
  {:else if list.runs.length === 0}
    <EmptyState title="No workflows yet">Start one with <strong>Run workflow</strong>. Agents in this room start them with <span class="mono">dyna run</span>.</EmptyState>
  {:else}
    {#if running.length}
      <h2>Running now</h2>
      <div class="cards">
        {#each running as r (r.id)}
          <button class="card" onclick={() => onopen(r)}>
            <div class="top">
              <RunIcon status="running" size={20} />
              <span class="name">{r.name}</span>
              <span class="when">{duration(r.startedAt, null)}</span>
            </div>
            <div class="bar" role="progressbar" aria-valuenow={pct(r)} aria-valuemin="0" aria-valuemax="100"><span class="fill" style="width:{pct(r)}%"></span>{#if r.failed}<span class="failfill" style="width:{(r.failed / r.agents) * 100}%"></span>{/if}</div>
            <div class="meta">
              <span>{r.done + r.failed} of {r.agents} agents done</span>
              {#if r.working}<span class="gold">{r.working} working</span>{/if}
              {#if r.failed}<span class="bad">{r.failed} failed</span>{/if}
              <span class="spacer"></span>
              {#if r.parent}<span>by {r.parent}</span>{/if}
            </div>
          </button>
        {/each}
      </div>
    {/if}

    {#if recent.length}
      <h2>Recent</h2>
      <div class="table">
        {#each recent as r (r.id)}
          <button class="row" onclick={() => onopen(r)}>
            <RunIcon status={r.status} size={18} />
            <span class="main">
              <span class="name">{r.name}</span>
              {#if r.error && r.status !== 'canceled'}
                <span class="sub bad">{r.error}</span>
              {:else}
                <span class="sub">{r.agents} agent{r.agents === 1 ? '' : 's'}{#if r.parent}&nbsp;· by {r.parent}{/if}<span class="mono id">&nbsp;· {r.id}</span></span>
              {/if}
            </span>
            <span class="st {r.status}">{label[r.status] ?? r.status}</span>
            <span class="when">
              <span>{ago(r.startedAt)}</span>
              <span class="dur">{duration(r.startedAt, r.endedAt)}</span>
            </span>
          </button>
        {/each}
      </div>
    {/if}
    {#if list.total > list.runs.length}
      <p class="quiet">Showing the newest {list.runs.length} of {list.total} runs. <span class="mono">dyna runs list</span> shows all of them.</p>
    {/if}
  {/if}
  </div>
</section>

<style>
  .runs { flex: 1; overflow-y: auto; min-height: 0; }
  .body { padding: 0 var(--s5) var(--s5); }
  .start { display: inline-flex; align-items: center; gap: 7px; flex: none; padding: 8px 14px 8px 11px; border-radius: 7px; background: var(--iris); color: #18161d; font-size: 14px; font-weight: 600; transition: background-color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .start:hover { background: var(--iris-hover); }
  }
  .start:active { background: var(--select); color: var(--ink); }
  h2 { font-size: 12px; font-weight: 700; color: var(--faint); text-transform: uppercase; letter-spacing: 0.06em; margin: 22px 0 10px; }
  .gold { color: var(--gold) !important; }
  .bad { color: var(--coral) !important; }
  .quiet { color: var(--faint); font-size: 13.5px; padding: 8px 0; }
  .err { color: var(--coral); }
  .cards { display: grid; grid-template-columns: repeat(auto-fill, minmax(320px, 1fr)); gap: 10px; }
  .card { text-align: left; background: var(--side); border: 1px solid color-mix(in srgb, var(--gold) 22%, var(--line-soft)); border-radius: 12px; padding: 14px 16px; display: flex; flex-direction: column; gap: 10px; transition: border-color var(--motion-fast), transform var(--motion-fast); }
  @media (hover: hover) {
    .card:hover { border-color: color-mix(in srgb, var(--gold) 45%, transparent); transform: translateY(-1px); }
  }
  .top { display: flex; align-items: center; gap: 10px; }
  .top .name { font-weight: 650; font-size: 15.5px; flex: 1; min-width: 0; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .top .when { font-size: 12.5px; color: var(--muted); font-variant-numeric: tabular-nums; }
  .bar { position: relative; height: 6px; border-radius: 3px; background: var(--rail); overflow: hidden; }
  .fill { position: absolute; inset: 0 auto 0 0; background: linear-gradient(90deg, #b59654, var(--gold)); border-radius: 3px; transition: width var(--motion-slow); }
  .failfill { position: absolute; inset: 0 0 0 auto; background: var(--coral); opacity: 0.8; }
  .meta { display: flex; gap: 12px; font-size: 12.5px; color: var(--muted); }
  .spacer { flex: 1; }
  .table { display: flex; flex-direction: column; background: var(--side); border: 1px solid var(--line-soft); border-radius: 12px; overflow: hidden; }
  .row { display: grid; grid-template-columns: 18px 1fr auto 92px; align-items: center; gap: 14px; padding: 12px 16px; text-align: left; transition: background-color var(--motion-fast) var(--ease); }
  .row + .row { border-top: 1px solid var(--line-soft); }
  @media (hover: hover) {
    .row:hover { background: var(--hover); }
  }
  .row:active { background: var(--select); }
  .main { display: flex; flex-direction: column; min-width: 0; gap: 2px; }
  .row .name { font-weight: 600; font-size: 14.5px; color: var(--ink); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .sub { font-size: 12.5px; color: var(--faint); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .id { font-size: 11.5px; }
  .st { font-size: 12px; font-weight: 600; padding: 2px 8px; border-radius: 10px; background: var(--raise); color: var(--muted); }
  .st.ok { color: var(--sage); background: rgba(151, 201, 149, 0.1); }
  .st.error { color: var(--coral); background: rgba(239, 138, 128, 0.1); }
  .when { display: flex; flex-direction: column; align-items: flex-end; font-size: 12.5px; color: var(--muted); }
  .dur { color: var(--faint); font-size: 12px; font-variant-numeric: tabular-nums; }
  @media (max-width: 760px) {
    .body { padding: 0 var(--s4) var(--s4); }
    .start { min-height: 44px; }
    .cards { grid-template-columns: 1fr; }
  }
</style>
