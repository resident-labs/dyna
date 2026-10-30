<script lang="ts">
  import StatusPill from "./StatusPill.svelte";
  import { formatTime, type Row } from "../feed.ts";
  let { row, author }: { row: Extract<Row, { kind: "failed" }>; author: string } = $props();
</script>

<p class="failed"><strong>{author}</strong> <time datetime={row.at}>{formatTime(row.at)}</time> <StatusPill tone="failed">Turn failed</StatusPill>
  {#if row.error}<span class="error">{row.error}</span>{/if}</p>

<style>
  .failed { margin: 0; padding: 8px 16px 2px; display: flex; flex-wrap: wrap; align-items: baseline; gap: 8px; }
  .error { color: var(--muted); }
  time { color: var(--muted); font-size: 12px; font-variant-numeric: tabular-nums; }
</style>
