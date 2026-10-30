<script lang="ts">
  import type { NodeHistoryPoint } from '../api/machine.ts'
  import { historySegments } from './nodeHistory.ts'
  let { points }: { points: NodeHistoryPoint[] } = $props()
  let now = $state(Date.now())
  $effect(() => { now = Date.now() })
  let segments = $derived(historySegments(points, now))
</script>

{#if points.length >= 3 && segments.some((s) => s.cpu || s.memory)}
  <div class="history">
    <p>CPU and memory · last 24 hours</p>
    <svg viewBox="0 0 100 100" preserveAspectRatio="none" role="img" aria-label="CPU and memory use over the last 24 hours, from 0 to 100 percent">
      <line class="guide" x1="0" y1="1" x2="100" y2="1" /><line class="guide" x1="0" y1="99" x2="100" y2="99" />
      {#each segments as line}
        {#if line.cpu}<polyline points={line.cpu} class="cpu" />{/if}
        {#if line.memory}<polyline points={line.memory} class="memory" />{/if}
      {/each}
    </svg>
    <p><span class="key"><i class="cpu-dot"></i>CPU</span> · <span class="key"><i class="memory-dot"></i>Memory</span></p>
  </div>
{:else}<p class="note">Collecting history · first samples in 5 min</p>{/if}

<style>
  .history { margin-top: var(--s2); }
  p { margin: 0; color: var(--muted); font-size: var(--t-cap); }
  svg { width: 100%; height: 64px; display: block; }
  .guide { stroke: var(--muted); opacity: .18; vector-effect: non-scaling-stroke; }
  polyline { fill: none; stroke-width: 1; vector-effect: non-scaling-stroke; }
  .cpu { stroke: var(--data-1); } .memory { stroke: var(--data-2); }
  .key { display: inline-flex; align-items: center; gap: var(--s1); }
  .key i { width: 8px; height: 8px; border-radius: 50%; }
  .cpu-dot { background: var(--data-1); } .memory-dot { background: var(--data-2); }
  .note { color: var(--faint); margin: 0; }
</style>
