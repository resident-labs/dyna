<script lang="ts">
  import StatusPill from "./StatusPill.svelte";
  import type { Row } from "../feed.ts";
  let { row, author }: { row: Extract<Row, { kind: "failed" }>; author: string } = $props();
</script>

<p class="failed"><strong>{author}</strong> <StatusPill tone="failed">Turn failed</StatusPill>
  {#if row.error}<span class="error">{row.error}</span>{/if}</p>

<style>
  .failed { margin: 0; padding: 8px 16px 2px; display: flex; flex-wrap: wrap; align-items: baseline; gap: 8px; }
  .error { color: var(--muted); }
</style>
