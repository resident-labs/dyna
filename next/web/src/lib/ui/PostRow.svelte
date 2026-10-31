<script lang="ts" module>
  import DOMPurify from "dompurify";
  // Links in posts open in a new tab without access to this page.
  DOMPurify.addHook("afterSanitizeAttributes", node => {
    if (node instanceof HTMLAnchorElement) {
      node.target = "_blank";
      node.rel = "noopener noreferrer";
    }
  });
</script>

<script lang="ts">
  import { marked } from "marked";
  import Avatar from "./Avatar.svelte";
  import { formatTime, type Row } from "../feed.ts";
  import { hue } from "../identity.ts";
  let { row, author, to }: { row: Extract<Row, { kind: "post" }>; author: string; to?: string } = $props();
  // Markdown from any author is sanitized before it reaches the page. Images wait for the files view.
  let html = $derived(DOMPurify.sanitize(marked.parse(row.text, { async: false, gfm: true, breaks: true }), { FORBID_TAGS: ["img"] }));
</script>

<article class="post" class:first={row.first}>
  {#if row.first}
    <Avatar kind={row.from.kind} name={author} />
    <p class="name"><strong class={row.from.kind} style:--hue={hue(author)}>{author}</strong>
      <time datetime={row.at}>{formatTime(row.at)}</time>{#if to}<span class="to">→ {to}</span>{/if}</p>
  {/if}
  <div class="text">{@html html}</div>
  {#if row.attachments.length}
    <p class="files">{row.attachments.length === 1 ? "1 file" : `${row.attachments.length} files`}: {row.attachments.map(file => file.name).join(", ")}</p>
  {/if}
</article>

<style>
  /* The avatar sits in a 32 px gutter; later posts of a run align under the first one's text. */
  .post { display: grid; grid-template-columns: 32px minmax(0, 1fr); column-gap: 12px; padding: 1px 16px; }
  .post:hover { background: var(--hover); }
  .post > :not(:global(.avatar)) { grid-column: 2; }
  .post > :global(.avatar) { grid-row: 1 / span 3; margin-top: 2px; }
  .first { padding-top: 14px; }
  .name { margin: 0; display: flex; align-items: baseline; gap: 8px; }
  strong { color: oklch(var(--id-name) var(--hue)); }
  strong.owner { color: var(--focus); }
  strong.engine { color: var(--text); }
  time, .to, .files { color: var(--muted); font-size: 12px; font-variant-numeric: tabular-nums; }
  .files { margin: 2px 0 0; }
  .text { overflow-wrap: anywhere; }
  .text :global(:is(p, ul, ol, pre, blockquote)) { margin: 0 0 4px; }
  .text :global(:is(p, ul, ol, pre, blockquote):last-child) { margin-bottom: 0; }
  .text :global(pre) { padding: 8px 10px; border-radius: 8px; background: var(--sidebar); overflow-x: auto; }
  .text :global(code) { font: 13px ui-monospace, monospace; }
  .text :global(blockquote) { padding-left: 10px; border-left: 2px solid var(--line); color: var(--muted); }
  .text :global(a) { color: var(--focus); }
</style>
