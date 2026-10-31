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
  import { fileSize, formatTime, isImage, type Row } from "../feed.ts";
  import type { Attachment } from "../contract.gen.ts";
  import { hue } from "../identity.ts";
  let { row, author, to, root }: { row: Extract<Row, { kind: "post" }>; author: string; to?: string; root: boolean } = $props();
  // Markdown from any author is sanitized before it reaches the page. Images wait for the files view.
  let html = $derived(DOMPurify.sanitize(marked.parse(row.text, { async: false, gfm: true, breaks: true }), { FORBID_TAGS: ["img"] }));
  // The engine serves each file sandboxed; the owner's cookie covers the request.
  const url = (file: Attachment) => `/v1/files/${encodeURIComponent(file.sha256)}`;
  // The type badge on a file card, such as "TXT", as in v1.
  const extension = (name: string) => /\.([A-Za-z0-9]{1,6})$/.exec(name)?.[1].toUpperCase() ?? "FILE";
  let images = $derived(row.attachments.filter(isImage));
  let others = $derived(row.attachments.filter(file => !isImage(file)));
</script>

<article class="post" class:first={row.first}>
  {#if row.first}
    <Avatar kind={row.from.kind} name={author} {root} />
    <p class="name"><strong class={row.from.kind} style:--hue={hue(author)}>{author}</strong>
      <time datetime={row.at}>{formatTime(row.at)}</time>{#if to}<span class="to">→ {to}</span>{/if}</p>
  {/if}
  <div class="text">{@html html}</div>
  <!-- As in v1, images come first as thumbnails, then a card per other file. Each opens the file in a new tab. -->
  {#if images.length}<div class="images" class:many={images.length > 1}>{#each images as file, i (i)}
    <a class="thumb" href={url(file)} target="_blank" rel="noopener" title={`${file.name} · ${fileSize(file.bytes)}`}>
      <img src={url(file)} alt={file.name} loading="lazy" /></a>{/each}</div>{/if}
  {#each others as file, i (i)}
    <a class="file" href={url(file)} target="_blank" rel="noopener" title={`Open ${file.name}`}>
      <span class="ext">{extension(file.name)}</span>
      <span class="meta"><span class="file-name">{file.name}</span><span class="size">{fileSize(file.bytes)}</span></span>
      <span class="open" aria-hidden="true">↗</span></a>{/each}
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
  time, .to { color: var(--muted); font-size: 12px; font-variant-numeric: tabular-nums; }
  /* v1's sizes: one image up to 420x300, several as 200x150 crops; a 420 px file card with a 40 px type badge. */
  .images { display: flex; flex-wrap: wrap; gap: 6px; margin: 6px 0 2px; }
  .thumb { display: block; max-width: min(420px, 100%); border-radius: 8px; overflow: hidden; background: var(--sidebar); line-height: 0; }
  .thumb img { display: block; max-width: 100%; max-height: 300px; object-fit: contain; }
  .many .thumb { width: 200px; height: 150px; }
  .many .thumb img { width: 100%; height: 100%; max-height: none; object-fit: cover; }
  .file { display: flex; align-items: center; gap: 12px; width: min(420px, 100%); margin: 6px 0 2px; padding: 10px 12px;
    background: var(--sidebar); border: 1px solid var(--line); border-radius: 8px; color: var(--text); text-decoration: none; }
  .file:hover { background: var(--hover); }
  .ext { display: grid; place-items: center; flex: none; width: 40px; height: 40px; border-radius: 6px; background: var(--raised);
    color: var(--muted); font: 600 10.5px var(--mono); }
  .meta { display: flex; flex-direction: column; min-width: 0; }
  .file-name { font-weight: 500; font-size: 14px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .size { color: var(--muted); font-size: 12px; }
  .open { margin-left: auto; color: var(--muted); font-size: 16px; }
  .file:hover .open { color: var(--text); }
  .thumb:hover, .thumb:focus-visible, .file:focus-visible { outline: 2px solid var(--focus); outline-offset: 1px; }
  .text { overflow-wrap: anywhere; }
  .text :global(:is(p, ul, ol, pre, blockquote)) { margin: 0 0 4px; }
  .text :global(:is(p, ul, ol, pre, blockquote):last-child) { margin-bottom: 0; }
  .text :global(pre) { padding: 8px 10px; border-radius: 8px; background: var(--sidebar); overflow-x: auto; }
  .text :global(code) { font: 13px var(--mono); font-variant-ligatures: none; }
  .text :global(blockquote) { padding-left: 10px; border-left: 2px solid var(--line); color: var(--muted); }
  .text :global(a) { color: var(--focus); }
</style>
