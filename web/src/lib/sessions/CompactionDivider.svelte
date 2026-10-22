<script lang="ts">
  // One full-width divider for a context compaction: "── Context compacted ·
  // 182k → 41k tokens · auto · 14:02 ──". While it runs, a small spinner
  // replaces the rule's text; a failure shows the reason in the error color.
  import { compactionText } from './compaction.ts'
  import type { CompactionItem } from './compaction.ts'
  import { clock } from '../ui/format.js'

  interface Props {
    row: CompactionItem & { ts?: number | string }
    enter?: boolean
  }
  let { row, enter = false }: Props = $props()
  let failed = $derived(row.state === 'failed')
  let running = $derived(row.state === 'started')
</script>

<div class="compaction" class:enter class:failed class:running role="separator" aria-label={compactionText(row)}>
  <span>
    {#if running}<i class="spin" aria-hidden="true"></i>{/if}{compactionText(row)}{#if !running && row.ts}<time> · {clock(row.ts)}</time>{/if}
  </span>
</div>

<style>
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
    .compaction { margin: 18px var(--s3) 8px; }
    .compaction span { font-size: var(--t-cap); }
  }
</style>
