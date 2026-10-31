<script lang="ts">
  import StatusPill from "./StatusPill.svelte";
  import { formatTime, type Row } from "../feed.ts";
  let { row, author }: { row: Extract<Row, { kind: "failed" }>; author: string } = $props();
</script>

<p class="failed"><strong>{author}</strong> <time datetime={row.at}>{formatTime(row.at)}</time> <StatusPill tone="failed">{row.label}</StatusPill>
  {#if row.cause}<span class="cause">{row.cause}</span>{/if}</p>

<style>
  .failed { margin: 0; padding: 8px 16px 2px var(--gutter); display: flex; flex-wrap: wrap; align-items: baseline; gap: 8px; }
  .cause { color: var(--muted); }
  time { color: var(--muted); font-size: 12px; font-variant-numeric: tabular-nums; }
</style>
