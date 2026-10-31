<script lang="ts">
  import Avatar from "./Avatar.svelte";
  import StatusPill from "./StatusPill.svelte";
  import { formatTime, type Row } from "../feed.ts";
  import { hue } from "../identity.ts";
  let { row, author, root }: { row: Extract<Row, { kind: "failed" }>; author: string; root: boolean } = $props();
</script>

<!-- As v1's turn result, a failed turn uses the post layout: the session's avatar, then its name line and the cause. -->
<article class="failed">
  <Avatar kind="session" name={author} {root} />
  <p class="name"><strong style:--hue={hue(author)}>{author}</strong> <time datetime={row.at}>{formatTime(row.at)}</time>
    <StatusPill tone="failed">{row.label}</StatusPill></p>
  {#if row.cause}<p class="cause">{row.cause}</p>{/if}
</article>

<style>
  .failed { display: grid; grid-template-columns: 32px minmax(0, 1fr); column-gap: 12px; padding: 14px 16px 1px; }
  .failed > :global(.avatar) { grid-row: 1 / span 2; margin-top: 2px; }
  p { grid-column: 2; margin: 0; }
  .name { display: flex; align-items: baseline; gap: 8px; }
  strong { color: oklch(var(--id-name) var(--hue)); }
  .cause { color: var(--muted); }
  time { color: var(--muted); font-size: 12px; font-variant-numeric: tabular-nums; }
</style>
