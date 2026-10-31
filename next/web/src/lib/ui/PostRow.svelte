<script lang="ts" module>
  import DOMPurify from "dompurify";
  import { marked } from "marked";
  // Links in posts open in a new tab without access to this page.
  DOMPurify.addHook("afterSanitizeAttributes", node => {
    if (node instanceof HTMLAnchorElement) {
      node.target = "_blank";
      node.rel = "noopener noreferrer";
    }
  });
  // Markdown from any author is sanitized before it reaches the page. Images wait for the files view. Each text renders
  // once: a row that scrolls back in, or gets a new row object when a page loads, reuses its HTML. Parsing and
  // sanitizing cost about a millisecond a post, which was most of a scroll frame. The feed keeps at most 1,000 events,
  // so a full cache starts over.
  const rendered = new Map<string, string>();
  export function render(text: string): string {
    let html = rendered.get(text);
    if (html === undefined) {
      if (rendered.size >= 2000) rendered.clear();
      html = DOMPurify.sanitize(marked.parse(text, { async: false, gfm: true, breaks: true }), { FORBID_TAGS: ["img"] });
      rendered.set(text, html);
    }
    return html;
  }
</script>

<script lang="ts">
  import Avatar from "./Avatar.svelte";
  import Icon from "./Icon.svelte";
  import { fileSize, formatTime, isImage, isThumb, type Row } from "../feed.ts";
  import type { Attachment } from "../contract.gen.ts";
  import { hue } from "../identity.ts";
  let { row, author, to, root, onretry, ondiscard, onforward }: {
    row: Extract<Row, { kind: "post" }>; author: string; to?: string; root: boolean; onretry: (id: number) => void; ondiscard: (id: number) => void; onforward?: () => void;
  } = $props();
  let html = $derived(render(row.text));
  // Copy text answers in the bar for 2 seconds, as in v1.
  let copied = $state("");
  let timer: ReturnType<typeof setTimeout> | undefined;
  function copy() {
    clearTimeout(timer);
    navigator.clipboard.writeText(row.text).then(() => { copied = "Copied"; }, () => { copied = "Could not copy"; })
      .finally(() => { timer = setTimeout(() => { copied = ""; }, 2000); });
  }
  // The engine serves each file sandboxed; the owner's cookie covers the request.
  const url = (file: Attachment) => `/v1/files/${encodeURIComponent(file.sha256)}`;
  // The type badge on a file card, such as "TXT", as in v1.
  const extension = (name: string) => /\.([A-Za-z0-9]{1,6})$/.exec(name)?.[1].toUpperCase() ?? "FILE";
  // Images the engine refused. Each shows as a file card without a link, as its link would open only the refusal.
  let failed: string[] = $state([]);
  // Images already retried once; a second failure leaves the card without Retry.
  let retried: string[] = $state([]);
  const retry = (file: Attachment) => { retried = [...retried, file.sha256]; failed = failed.filter(sha => sha !== file.sha256); };
  let images = $derived(row.attachments.filter(file => isThumb(file, failed)));
  let others = $derived(row.attachments.filter(file => !isThumb(file, failed)));
</script>

<!-- A tap focuses the row on a touch screen, so focus shows its action bar as hover does. -->
<article class="post" class:first={row.first} class:pending={row.pending} aria-busy={row.pending && !row.pending.failed} tabindex="-1">
  {#if !row.pending}<div class="bar"><span class="status" role="status">{copied}</span>
    {#if onforward}<button type="button" aria-label="Forward" title="Forward" onclick={onforward}><Icon name="forward" /></button>{/if}
    <button type="button" aria-label="Copy text" title="Copy text" onclick={copy}><Icon name={copied === "Copied" ? "check" : "copy"} /></button></div>{/if}
  {#if row.first}
    <Avatar kind={row.from.kind} name={author} {root} />
    <p class="name"><strong class={row.from.kind} style:--hue={hue(author)}>{author}</strong>
      <time datetime={row.at}>{formatTime(row.at)}</time>{#if to}<span class="to">→ {to}</span>{/if}</p>
  {/if}
  <div class="text">{@html html}</div>
  <!-- As in v1, images come first as thumbnails, then a card per other file. Each opens the file in a new tab. -->
  {#if images.length}<div class="images" class:many={images.length > 1}>{#each images as file, i (i)}
    <a class="thumb" href={url(file)} target="_blank" rel="noopener" title={`${file.name} · ${fileSize(file.bytes)}`}>
      <img src={url(file)} alt={file.name} loading="lazy" onerror={() => { failed = [...failed, file.sha256]; }} /></a>{/each}</div>{/if}
  {#each others as file, i (i)}{@const dead = failed.includes(file.sha256)}
    <svelte:element this={dead ? "span" : "a"} class="file" class:dead href={dead ? undefined : url(file)} target={dead ? undefined : "_blank"} rel={dead ? undefined : "noopener"}
      title={dead ? `${file.name} did not load` : `Open ${file.name}`}>
      <span class="ext" aria-hidden="true">{extension(file.name)}</span>
      <span class="meta"><span class="file-name">{file.name}</span><span class="size">{dead ? "Did not load" : fileSize(file.bytes)}</span></span>
      {#if !dead}<span class="open" aria-hidden="true">↗</span>
      {:else if isImage(file) && !retried.includes(file.sha256)}<button type="button" class="reload" onclick={() => retry(file)}>Retry</button>{/if}</svelte:element>{/each}
  {#if row.pending?.failed}{@const id = row.pending.id}<p class="unsent" role="alert">Not sent
    <button type="button" onclick={() => onretry(id)}>Retry</button><button type="button" onclick={() => ondiscard(id)}>Discard</button></p>{/if}
</article>

<style>
  /* The avatar sits in a 32 px gutter; later posts of a run align under the first one's text. */
  .post { display: grid; grid-template-columns: 32px minmax(0, 1fr); column-gap: 12px; padding: 1px 16px; }
  .post { position: relative; }
  .post:hover, .post:focus-within { background: var(--hover); }
  .post:focus { outline: none; }
  .post > :not(:global(.avatar)) { grid-column: 2; }
  /* v1's bar: it floats over the row's top right and is laid out while hidden, so showing it never moves the row. */
  .post > .bar { grid-column: auto; position: absolute; top: -14px; right: 16px; z-index: 1; display: flex; align-items: center; gap: 2px; padding: 2px;
    background: var(--raised); border: 1px solid var(--line); border-radius: 8px; box-shadow: 0 4px 14px var(--shadow-md); opacity: 0; pointer-events: none; }
  /* Hidden by opacity, not visibility, so Tab still reaches the buttons and shows the bar. */
  .post:hover > .bar, .post:focus-within > .bar { opacity: 1; pointer-events: auto; }
  .bar button { all: unset; display: grid; place-items: center; width: 28px; height: 26px; border-radius: 6px; color: var(--muted); cursor: pointer; }
  .bar button:hover { background: var(--selected); color: var(--text); }
  .bar button:focus-visible { outline: 2px solid var(--focus); outline-offset: 0; }
  @media (hover: none) { .bar button { width: 40px; height: 40px; } }
  .status { color: var(--muted); font-size: 12px; }
  .status:not(:empty) { padding: 0 6px; }
  .post > :global(.avatar) { grid-row: 1 / span 3; margin-top: 2px; }
  .first { padding-top: 14px; }
  /* A post the engine has not confirmed is dimmed; its Not sent line keeps full contrast in the danger color, because the owner must act. */
  .pending > :global(:not(.unsent)) { opacity: 0.5; }
  .unsent { margin: 2px 0 0; display: flex; align-items: baseline; gap: 12px; color: var(--danger); font-size: 12px; }
  .unsent button { all: unset; cursor: pointer; color: var(--focus); border-radius: 3px; }
  .unsent button:hover { text-decoration: underline; }
  .unsent button:focus-visible { outline: 2px solid var(--focus); outline-offset: 2px; }
  .name { margin: 0; display: flex; align-items: baseline; gap: 8px; }
  strong { color: oklch(var(--id-name) var(--hue)); }
  strong.owner { color: var(--focus); }
  strong.engine { color: var(--text); }
  time, .to { color: var(--muted); font-size: 12.5px; font-variant-numeric: tabular-nums; }
  /* v1's sizes: one image up to 420x300, several as 200x150 crops; a 420 px file card with a 40 px type badge. */
  .images { display: flex; flex-wrap: wrap; gap: 6px; margin: 6px 0 2px; }
  .thumb { display: block; max-width: min(420px, 100%); border-radius: 8px; overflow: hidden; background: var(--sidebar); line-height: 0; }
  .thumb img { display: block; max-width: 100%; max-height: 300px; object-fit: contain; }
  .many .thumb { width: 200px; height: 150px; }
  .many .thumb img { width: 100%; height: 100%; max-height: none; object-fit: cover; }
  .file { display: flex; align-items: center; gap: 12px; width: min(420px, 100%); margin: 6px 0 2px; padding: 10px 12px;
    background: var(--sidebar); border: 1px solid var(--line); border-radius: 8px; color: var(--text); text-decoration: none; }
  .file:hover { background: var(--hover); }
  /* A file that did not load is plain text: nothing to open. */
  .file.dead { cursor: default; }
  .file.dead .file-name { color: var(--text); }
  .reload { margin-left: auto; padding: 0; border: 0; background: none; color: var(--muted); font: inherit; font-size: 12px; cursor: pointer; }
  .reload:hover { color: var(--text); text-decoration: underline; }
  .ext { display: grid; place-items: center; flex: none; width: 40px; height: 40px; border-radius: 6px; background: var(--raised);
    color: var(--muted); font: 600 10.5px var(--mono); }
  .meta { display: flex; flex-direction: column; min-width: 0; }
  .file-name { color: var(--focus); font-weight: 500; font-size: 14px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
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
