<script lang="ts" module>
  import DOMPurify from "dompurify";
  import { parse } from "../markdown.ts";
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
  function render(text: string): string {
    let html = rendered.get(text);
    if (html === undefined) {
      if (rendered.size >= 2000) rendered.clear();
      html = DOMPurify.sanitize(parse(text), { FORBID_TAGS: ["img"] });
      rendered.set(text, html);
    }
    return html;
  }
</script>

<script lang="ts">
  let { text }: { text: string } = $props();
  let html = $derived(render(text));
</script>

<div class="markdown">{@html html}</div>

<style>
  .markdown { overflow-wrap: anywhere; line-height: 1.45; }
  .markdown :global(:is(p, ul, ol, pre, blockquote)) { margin: 0 0 4px; }
  .markdown :global(:is(p, ul, ol, pre, blockquote):last-child) { margin-bottom: 0; }
  /* The type scale: post text is 15 px, a title 16 and a head 18. A heading is no larger than a section header. */
  .markdown :global(:is(h1, h2, h3, h4, h5, h6)) { margin: 8px 0 4px; font-size: 15px; line-height: 1.25; }
  .markdown :global(h1) { font-size: 18px; }
  .markdown :global(h2) { font-size: 16px; }
  .markdown :global(:is(h1, h2, h3, h4, h5, h6):first-child) { margin-top: 0; }
  /* A code block sits on the raised surface and scrolls sideways; it never wraps. */
  .markdown :global(pre) { padding: 8px 10px; border: 1px solid var(--line); border-radius: 8px; background: var(--raised); overflow-x: auto; white-space: pre; overflow-wrap: normal; }
  /* A wide table scrolls sideways inside its own block, as a code block does. Cells never wrap mid-word. */
  .markdown :global(table) { display: block; max-width: 100%; overflow-x: auto; border-collapse: collapse; }
  .markdown :global(:is(th, td)) { padding: 4px 8px; border: 1px solid var(--line); text-align: left; white-space: nowrap; }
  .markdown :global(code) { font: 13px var(--mono); font-variant-ligatures: none; }
  .markdown :global(blockquote) { padding-left: 10px; border-left: 2px solid var(--line); color: var(--muted); }
  .markdown :global(a) { color: var(--focus); }
</style>
