<script lang="ts">
  import { marked } from "marked";
  import DOMPurify from "dompurify";
  import type { Row } from "../feed.ts";
  let { row, author }: { row: Extract<Row, { kind: "post" }>; author: string } = $props();
  // Markdown from any author is sanitized before it reaches the page.
  let html = $derived(DOMPurify.sanitize(marked.parse(row.text, { async: false, gfm: true, breaks: true })));
  let time = $derived.by(() => {
    const at = new Date(row.at);
    const clock = at.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
    return at.toDateString() === new Date().toDateString() ? clock : `${at.toLocaleDateString([], { month: "short", day: "numeric" })}, ${clock}`;
  });
</script>

<article class="post" class:first={row.first}>
  {#if row.first}<p class="name"><strong>{author}</strong> <time datetime={row.at}>{time}</time></p>{/if}
  <div class="text">{@html html}</div>
  {#if row.attachments.length}
    <p class="files">{row.attachments.length === 1 ? "1 file" : `${row.attachments.length} files`}: {row.attachments.map(file => file.name).join(", ")}</p>
  {/if}
</article>

<style>
  .post { padding: 1px 16px; }
  .first { padding-top: 12px; }
  .name { margin: 0 0 2px; }
  time, .files { color: var(--muted); font-size: 12px; font-variant-numeric: tabular-nums; }
  .files { margin: 2px 0 0; }
  .text { overflow-wrap: anywhere; }
  .text :global(:is(p, ul, ol, pre, blockquote)) { margin: 0 0 4px; }
  .text :global(:is(p, ul, ol, pre, blockquote):last-child) { margin-bottom: 0; }
  .text :global(pre) { padding: 8px 10px; border-radius: 8px; background: var(--sidebar); overflow-x: auto; }
  .text :global(code) { font: 13px ui-monospace, monospace; }
  .text :global(blockquote) { padding-left: 10px; border-left: 2px solid var(--line); color: var(--muted); }
  .text :global(a) { color: var(--focus); }
</style>
