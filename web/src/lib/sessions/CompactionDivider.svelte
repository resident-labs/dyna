<script lang="ts">
  // One full-width divider for a context compaction: "── Context compacted ·
  // 182k → 41k tokens · auto · 14:02 ──". While it runs, a small spinner
  // replaces the rule's text; a failure shows the reason in the error color.
  import { compactionText } from './compaction.ts'
  import type { CompactionItem } from './compaction.ts'
  import { clock } from '../ui/format.ts'

  interface Props {
    row: CompactionItem & { ts?: number | string }
    enter?: boolean
  }
  let { row, enter = false }: Props = $props()
  let failed = $derived(row.state === 'failed')
  let running = $derived(row.state === 'started')
  let done = $derived(!failed && !running)
  let recordedAt = $derived(row.ts !== undefined && row.ts !== '' ? new Date(row.ts) : null)
  let validTime = $derived(recordedAt && Number.isFinite(recordedAt.getTime()) ? recordedAt : null)
  const hasCount = (value: number | undefined) => typeof value === 'number' && Number.isFinite(value)

</script>

{#if done}
  <details class="completed" class:enter>
    <summary><span class="summary-text"><i class="mark" aria-hidden="true">⇣</i> {compactionText(row)}{#if validTime}<time> · {clock(row.ts)}</time>{/if}</span><span class="more">Details</span></summary>
    <dl>
      {#if hasCount(row.tokensBefore)}<dt>Tokens before</dt><dd>{row.tokensBefore?.toLocaleString()}</dd>{/if}
      {#if hasCount(row.tokensAfter)}<dt>Tokens after</dt><dd>{row.tokensAfter?.toLocaleString()}</dd>{/if}
      {#if row.trigger}<dt>Trigger</dt><dd>{row.trigger}</dd>{/if}
      {#if validTime}<dt>Recorded at</dt><dd><time datetime={validTime.toISOString()}>{validTime.toLocaleString(undefined, { year: 'numeric', month: 'short', day: '2-digit', hour: '2-digit', minute: '2-digit', second: '2-digit', timeZoneName: 'short' })}</time></dd>{/if}
      {#if !hasCount(row.tokensBefore) && !hasCount(row.tokensAfter) && !row.trigger && !validTime}<dd>No additional details recorded.</dd>{/if}
    </dl>
  </details>
{:else}
<div class="compaction" class:enter class:failed class:running class:done role="separator" aria-label={compactionText(row)}>
  <span>
    {#if running}<i class="spin" aria-hidden="true"></i>{:else}<i class="mark" aria-hidden="true">{failed ? '!' : '⇣'}</i>{/if}{compactionText(row)}{#if !running && row.ts}<time> · {clock(row.ts)}</time>{/if}
  </span>
</div>

{/if}

<style>
  .completed { margin: 22px var(--s4) 10px; }
  summary { display: flex; align-items: center; gap: var(--s2); cursor: pointer; color: var(--ink-2); font-size: var(--t-sm); list-style: none; }
  summary::-webkit-details-marker { display: none; }
  summary::before, summary::after { content: ''; border-top: 1px solid var(--line); flex: 1; min-width: 8px; }
  .summary-text { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .completed .mark, .more { color: var(--iris); }
  .more { flex: none; font-size: var(--t-cap); }
  .more::after { content: ' ▸'; }
  details[open] .more::after { content: ' ▾'; }
  dl { display: grid; grid-template-columns: auto minmax(0, 1fr); gap: var(--s2) var(--s3); margin: var(--s3) 0 0; padding: var(--s3); border: 1px solid var(--line-soft); border-radius: var(--r-xs); font-size: var(--t-sm); }
  dt { color: var(--muted); } dd { margin: 0; color: var(--ink-2); overflow-wrap: anywhere; }
  /* Full width, above and below every message and activity row. */
  .compaction {
    display: flex;
    align-items: center;
    border-top: 1px solid var(--line);
    margin: 22px var(--s4) 10px;
    height: 0;
  }
  .compaction span {
    display: flex;
    align-items: center;
    gap: 6px;
    background: var(--main);
    padding: 0 var(--s2);
    font-size: var(--t-sm);
    color: var(--faint);
    transform: translateY(-50%);
    max-width: 100%;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .compaction time { font-variant-numeric: tabular-nums; }
  /* A finished compaction is its own event, not the green "finished" turn line, so
     it reads in the ink colour with its own mark. */
  .compaction.done span { color: var(--ink-2); }
  .compaction.done .mark { color: var(--iris); }
  .mark { font-style: normal; font-size: var(--t-cap); }
  .compaction.failed .mark { color: var(--coral); }
  .compaction.failed { border-top-color: var(--coral); }
  .compaction.failed span { color: var(--coral); }
  /* A failure carries its reason, which can be long: it wraps on its own line
     instead of being clipped on a narrow screen. */
  .compaction.failed {
    height: auto;
    margin-bottom: var(--s2);
  }
  .compaction.failed span {
    transform: none;
    white-space: normal;
    padding: var(--s1) var(--s2);
    line-height: 1.35;
  }
  /* The spinner turns while the harness compacts: a quiet sign of work. */
  .spin {
    width: 9px;
    height: 9px;
    border: 1.5px solid var(--line);
    border-top-color: var(--muted);
    border-radius: 50%;
    animation: spin 1s linear infinite;
  }
  @keyframes spin { to { transform: rotate(360deg); } }
  @media (prefers-reduced-motion: reduce) { .spin { animation: none; } }
  @media (max-width: 760px) {
    .completed { margin: 18px var(--s3) 8px; }
    .compaction { margin: 18px var(--s3) 8px; }
    .compaction span { font-size: var(--t-cap); }
  }
</style>
