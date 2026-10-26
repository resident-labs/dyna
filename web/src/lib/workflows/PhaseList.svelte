<script lang="ts">
  // The phases of a run that has no shared stages: one row per phase, in run
  // order, with a progress bar and a count. A row opens to show its agents.
  import type { Snippet } from 'svelte'
  import RunIcon from './RunIcon.svelte'
  import type { CellState } from './lanes.ts'

  export interface PhaseRow {
    phase: string
    state: CellState
    done: number
    total: number
  }
  interface Props {
    rows: PhaseRow[]
    open: number[]
    ontoggle: (index: number) => void
    agents: Snippet<[number]>
  }
  let { rows, open, ontoggle, agents }: Props = $props()
</script>

<ol class="phases" aria-label="Phases">
  {#each rows as row, i}
    {@const isOpen = open.includes(i)}
    <li class={row.state}>
      <button class="row" aria-expanded={isOpen} onclick={() => ontoggle(i)}>
        <RunIcon status={row.state} size={16} />
        <span class="name">{row.phase}</span>
        <span class="bar" aria-hidden="true"><span class="fill {row.state}" style="width:{row.total ? (row.done / row.total) * 100 : 0}%"></span></span>
        <span class="count">{row.done}/{row.total}</span>
        <span class="chev" aria-hidden="true">›</span>
      </button>
      {#if isOpen}<div class="body">{@render agents(i)}</div>{/if}
    </li>
  {/each}
</ol>

<style>
  .phases { list-style: none; margin: 18px 0 4px; padding: 0; background: var(--side); border: 1px solid var(--line-soft); border-radius: 12px; }
  li + li { border-top: 1px solid var(--line-soft); }
  .row { display: grid; grid-template-columns: 16px minmax(0, 1fr) minmax(40px, 120px) auto 14px; align-items: center; column-gap: 12px; width: 100%; min-height: 44px; padding: 8px 14px; text-align: left; color: var(--ink-2); font-size: 13.5px; font-weight: 600; transition: background-color var(--motion-fast) var(--ease); }
  .name { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .bar { height: 5px; border-radius: 3px; background: var(--rail); overflow: hidden; }
  .fill { display: block; height: 100%; background: var(--sage); }
  .fill.running { background: var(--gold); }
  .fill.error { background: var(--coral); }
  .count { font-weight: 500; font-size: 12px; color: var(--faint); font-variant-numeric: tabular-nums; }
  li.running .count { color: var(--gold); }
  li.error .count { color: var(--coral); }
  .chev { color: var(--faint); transition: transform var(--motion-fast) var(--ease); }
  .row[aria-expanded='true'] .chev { transform: rotate(90deg); }
  .body { padding: 0 10px 10px; }
  @media (hover: hover) {
    .row:hover { background: var(--hover); }
  }
  .row:active { background: var(--select); }
  @media (max-width: 760px) {
    .row { grid-template-columns: 16px minmax(0, 1fr) auto 14px; }
    .bar { display: none; }
  }
</style>
