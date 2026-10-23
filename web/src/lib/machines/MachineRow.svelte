<script lang="ts">
  // One machine in the sidebar: its status, name and roles, a bar of its CPU
  // use and the figures it reports. It never lists the machine's agents: a
  // machine is a row, not a group of rows.
  import type { MachineSummary } from '../api/machine.ts'
  import { loadPct, roleLabel, statusText, summaryText } from './machines.ts'

  let { machine, selected = false, onopen }: {
    machine: MachineSummary
    selected?: boolean
    onopen: () => void
  } = $props()

  let pct = $derived(loadPct(machine))
  let counts = $derived(summaryText(machine))
</script>

<button class="row" class:sel={selected} onclick={onopen} aria-current={selected ? 'true' : undefined} title="{machine.name} · {statusText(machine.status)}">
  <span class="dot {machine.status}" aria-hidden="true"></span>
  <span class="body">
    <span class="top">
      <span class="name">{machine.name}</span>
      {#each machine.roles as role (role)}<span class="chip">{roleLabel(role)}</span>{/each}
    </span>
    <span class="bar" aria-hidden="true" class:idle={pct === null}>
      {#if pct !== null}<span class="fill" style="width: {pct}%"></span>{/if}
    </span>
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
  .dot { flex: none; width: 8px; height: 8px; border-radius: 50%; margin-top: 6px; background: var(--faint); }
  .dot.online { background: var(--sage); }
  .dot.off { background: var(--coral); }
  .body { flex: 1; min-width: 0; display: flex; flex-direction: column; gap: 4px; }
  .top { display: flex; align-items: center; gap: 5px; min-width: 0; }
  .name { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-weight: 600; }
  .chip {
    flex: none;
    padding: 0 5px;
    border-radius: 7px;
    background: var(--raise);
    color: var(--faint);
    font-size: var(--t-cap);
    font-weight: 600;
    line-height: 15px;
  }
  .row.sel .chip { background: var(--main); color: var(--muted); }
  .bar { display: block; height: 3px; border-radius: 2px; overflow: hidden; background: var(--raise); }
  .bar.idle { background: var(--line-soft); }
  .fill { display: block; height: 100%; background: var(--iris); }
  .counts { font-size: var(--t-cap); color: var(--faint); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
</style>
