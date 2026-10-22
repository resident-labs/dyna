<script lang="ts">
  // The count of what stops a session from finishing on its own, in a member
  // row. Hover shows the kinds in a small card. A touch screen has no
  // hover, so the kinds show under the count.
  import { linesOf, summaryOf, totalOf } from './responsibilities.ts'

  interface Props {
    counts?: unknown
  }
  let { counts = null }: Props = $props()
  let total = $derived(totalOf(counts))
  let lines = $derived(linesOf(counts))
</script>

{#if total}
  <span class="duty" role="note" aria-label="{total} {total === 1 ? 'responsibility' : 'responsibilities'}: {summaryOf(counts)}">
    <svg viewBox="0 0 16 16" width="11" height="11" aria-hidden="true"><path d="m4 6 4 4 4-4" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" /></svg>
    <b>{total}</b> {total === 1 ? 'responsibility' : 'responsibilities'}
    <span class="pop" aria-hidden="true">
      <span class="head">Stops it from finishing</span>
      {#each lines as line (line.kind)}<span class="k"><span class="name">{line.label}</span><span>{line.n}</span></span>{/each}
    </span>
    <span class="inline" aria-hidden="true">{#each lines as line (line.kind)}<span><b>{line.n}</b> {line.label}</span>{/each}</span>
  </span>
{/if}

<style>
  .duty { position: relative; display: inline-flex; flex-wrap: wrap; align-items: center; gap: 0 5px; margin-top: 3px; font-size: var(--t-cap); color: var(--muted); }
  .duty b { font-weight: 600; color: var(--ink-2); font-variant-numeric: tabular-nums; }
  .pop { display: none; position: absolute; left: 0; top: 100%; z-index: 5; margin-top: 4px; width: 176px; flex-direction: column; gap: 2px; padding: var(--s3); border: 1px solid var(--hair-strong); border-radius: var(--r-sm); background: rgba(var(--glass-ink), var(--glass-panel)); box-shadow: 0 8px 28px rgba(0, 0, 0, 0.45); font-size: var(--t-sm); color: var(--ink-2); }
  .head { margin-bottom: var(--s1); font-size: var(--t-cap); letter-spacing: 0.07em; text-transform: uppercase; color: var(--muted); }
  .k { display: flex; justify-content: space-between; }
  .k .name::first-letter { text-transform: uppercase; }
  .inline { display: none; flex-basis: 100%; flex-wrap: wrap; gap: 0 10px; }
  @media (hover: hover) { .duty:hover .pop { display: flex; } }
  @media (hover: none) { .inline { display: flex; } }
</style>
