<script lang="ts">
  import type { NodeHistoryPoint } from '../api/machine.ts'
  import { historySegments } from './nodeHistory.ts'
  let { points }: { points: NodeHistoryPoint[] } = $props()
  let now = $state(Date.now())
  $effect(() => { now = Date.now() })
  let segments = $derived(historySegments(points, now))
</script>

{#if points.length > 1}
  <div class="history">
    <p>CPU and memory · last 24 hours</p>
    <svg viewBox="0 0 100 100" preserveAspectRatio="none" role="img" aria-label="CPU and memory use over the last 24 hours">
      {#each segments as line}
        {#if line.cpu}<polyline points={line.cpu} class="cpu" />{/if}
        {#if line.memory}<polyline points={line.memory} class="memory" />{/if}
      {/each}
    </svg>
    <p><span class="cpu-label">CPU</span> · <span class="memory-label">Memory</span></p>
  </div>
{:else}<p class="note">History needs more readings.</p>{/if}

<style>
  .history { margin-top: var(--s2); }
  p { margin: 0; color: var(--muted); font-size: var(--t-cap); }
  svg { width: 100%; height: 64px; display: block; }
  polyline { fill: none; stroke-width: 1; vector-effect: non-scaling-stroke; }
  .cpu { stroke: var(--iris); } .memory { stroke: var(--hue-4); }
  .cpu-label { color: var(--iris); } .memory-label { color: var(--hue-4); }
  .note { color: var(--faint); margin: 0; }
</style>
