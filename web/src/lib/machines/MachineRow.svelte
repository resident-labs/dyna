<script lang="ts">
  // One machine in the sidebar: its status, name, a bar of its CPU use and
  // the figures it reports. It never lists the machine's agents: a machine is
  // a row, not a group of rows.
  import type { MachineSummary } from '../api/machine.ts'
  import { loadPct, statusText, summaryText } from './machines.ts'
  import { usageThresholds } from './nodeUsage.ts'

  let { machine, selected = false, onopen }: {
    machine: MachineSummary
    selected?: boolean
    onopen: () => void
  } = $props()

  let pct = $derived(loadPct(machine))
  let counts = $derived(summaryText(machine))
</script>

<button class="row" class:sel={selected} class:quiet={machine.status === 'disabled'} onclick={onopen} aria-current={selected ? 'true' : undefined} title="{machine.name} · {statusText(machine.status)}">
  <span class="dot {machine.status}" aria-hidden="true"></span>
  <span class="body">
    <span class="top">
      <span class="name">{machine.name}</span>
    </span>
    {#if pct !== null}
      <span class="bar" aria-hidden="true"><span class="fill" class:hot={pct >= usageThresholds.warn} style="width: {pct}%"></span></span>
    {/if}
    {#if machine.memoryPercent !== undefined}
      <span class="bar" aria-hidden="true"><span class="fill memory" class:hot={machine.memoryPercent >= usageThresholds.warn} style="width: {Math.max(0, Math.min(100, machine.memoryPercent))}%"></span></span>
    {/if}
    {#if counts}<span class="counts">{counts}</span>{/if}
  </span>
</button>

<style>
  .row {
    width: 100%;
    display: flex;
    align-items: flex-start;
    gap: var(--s2);
    padding: 6px var(--s2);
    margin: 1px 0;
    border-radius: 5px;
    color: var(--muted);
    text-align: left;
    transition: background-color var(--motion-fast) var(--ease), color var(--motion-fast) var(--ease);
  }
  @media (hover: hover) {
    .row:hover { background: var(--hover); color: var(--ink); }
  }
  .row:active { background: var(--select); }
  .row.sel { background: var(--select); color: var(--ink); }
  /* A switched off machine is quiet, not a fault: only an unreachable one is red. */
  .row.quiet .name { color: var(--muted); font-weight: 500; }
  .dot { flex: none; width: 8px; height: 8px; border-radius: 50%; margin-top: 6px; background: var(--faint); }
  .dot.online { background: var(--sage); }
  .dot.disabled { background: var(--faint); }
  .dot.offline { background: var(--coral); }
  /* Not probed yet: a ring, so it reads as unknown rather than quiet. */
  .dot.unknown { background: transparent; box-shadow: inset 0 0 0 1.5px var(--faint); }
  .body { flex: 1; min-width: 0; display: flex; flex-direction: column; gap: 4px; }
  .top { display: flex; align-items: center; gap: 5px; min-width: 0; }
  .name { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-weight: 600; }
  .bar { display: block; height: 3px; border-radius: 2px; overflow: hidden; background: var(--raise); }
  .fill { display: block; height: 100%; background: var(--iris); }
  .fill.memory { background: var(--hue-4); }
  .fill.hot { background: var(--gold); }
  .counts { font-size: var(--t-cap); color: var(--faint); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
</style>
