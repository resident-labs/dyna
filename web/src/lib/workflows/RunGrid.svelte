<script lang="ts">
  // The run's progress at a glance: one row per lane, one column per stage.
  // A cell shows its state and count; tap it to open its agents. Lanes that
  // failed or still run come first, and finished lanes fold into one row.
  import RunIcon from './RunIcon.svelte'
  import { byUrgency, otherStage, type CellRef, type LaneGrid } from './lanes.ts'

  interface Props {
    grid: LaneGrid
    picked?: CellRef | null
    onpick?: ((ref: CellRef | null) => void) | null
  }
  let { grid, picked = null, onpick = null }: Props = $props()

  let ordered = $derived(byUrgency(grid.lanes))
  let finished = $derived(ordered.filter((l) => l.state === 'ok'))
  // Folding only helps while other lanes still need a look.
  let foldable = $derived(finished.length > 1 && finished.length < ordered.length)
  let unfolded = $state(false)
  let shown = $derived(foldable && !unfolded ? ordered.filter((l) => l.state !== 'ok') : ordered)
  const stageName = (s: string) => s || 'other'
  const isPicked = (lane: string, col: number) => picked?.lane === lane && picked.col === col
</script>

<div class="grid" role="region" aria-label="Progress by lane and stage">
  <table>
    <thead>
      <tr>
        <th scope="col" class="lane"></th>
        {#each grid.stages as s (s)}<th scope="col" class:other={s === otherStage}>{stageName(s)}</th>{/each}
      </tr>
    </thead>
    <tbody>
      {#each shown as lane (lane.name)}
        <tr>
          <th scope="row" class="lane {lane.state}"><RunIcon status={lane.state} size={14} /><span>{lane.name}</span></th>
          {#each lane.cells as cell, col}
            <td>
              {#if cell}
                <button class="cell {cell.state}" class:picked={isPicked(lane.name, col)} aria-pressed={isPicked(lane.name, col)} aria-label="{lane.name} {stageName(grid.stages[col] ?? '')}: {cell.done} of {cell.total} done{cell.runs > 1 ? `, ${cell.runs} rounds` : ''}" onclick={() => onpick?.(isPicked(lane.name, col) ? null : { lane: lane.name, col })}>
                  <RunIcon status={cell.state} size={13} />
                  <span class="n">{cell.done}/{cell.total}</span>
                  {#if cell.runs > 1}<span class="x">×{cell.runs}</span>{/if}
                </button>
              {:else}
                <span class="none" aria-hidden="true">·</span>
              {/if}
            </td>
          {/each}
        </tr>
      {/each}
      {#if foldable}
        <tr>
          <td colspan={grid.stages.length + 1} class="fold">
            <button onclick={() => (unfolded = !unfolded)} aria-expanded={unfolded}>{unfolded ? 'Fold' : 'Show'} {finished.length} finished lane{finished.length === 1 ? '' : 's'}</button>
          </td>
        </tr>
      {/if}
    </tbody>
  </table>
</div>

<style>
  .grid { overflow-x: auto; background: var(--side); border: 1px solid var(--line-soft); border-radius: 12px; margin: 18px 0 4px; }
  table { border-collapse: separate; border-spacing: 0; width: 100%; }
  th, td { padding: 4px 6px; text-align: left; white-space: nowrap; }
  thead th { font-size: 11px; font-weight: 700; color: var(--faint); text-transform: uppercase; letter-spacing: 0.06em; padding-top: 10px; }
  .other { font-style: italic; }
  th.lane { position: sticky; left: 0; background: var(--side); font-size: 13px; font-weight: 600; color: var(--ink-2); padding-left: 12px; padding-right: 12px; z-index: 1; }
  th.lane span { margin-left: 7px; }
  tbody th.lane { display: table-cell; }
  .cell { display: inline-flex; align-items: center; gap: 6px; min-height: 32px; padding: 3px 10px; border-radius: 8px; background: var(--raise); border: 1px solid transparent; font-size: 12.5px; color: var(--ink-2); font-variant-numeric: tabular-nums; transition: background-color var(--motion-fast) var(--ease); }
  .cell.running { border-color: color-mix(in srgb, var(--gold) 40%, transparent); color: var(--gold); }
  .cell.error { border-color: color-mix(in srgb, var(--coral) 45%, transparent); color: var(--coral); background: color-mix(in srgb, var(--coral) 8%, var(--raise)); }
  .cell.picked { outline: 2px solid var(--iris); outline-offset: 1px; }
  .x { color: var(--faint); font-size: 11.5px; }
  .none { color: var(--line); padding: 0 12px; }
  .fold { padding: 6px 12px 10px; }
  .fold button { font-size: 12.5px; font-weight: 600; color: var(--muted); min-height: 32px; }
  @media (hover: hover) {
    .cell:hover { background: var(--raise-hover); }
    .fold button:hover { color: var(--ink); }
  }
  @media (max-width: 760px) {
    .cell { min-height: 44px; }
    .fold button { min-height: 44px; }
  }
</style>
