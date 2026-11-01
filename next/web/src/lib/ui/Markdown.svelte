<script lang="ts" module>
  import DOMPurify from "dompurify";
  import { parse } from "../markdown.ts";
  import { withCopyButtons } from "../codeCopy.ts";
  import { highlight } from "../highlight.ts";
  import { paths } from "./Icon.svelte";
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
      html = withCopyButtons(DOMPurify.sanitize(parse(text), { FORBID_TAGS: ["img"] }), paths.copy);
      rendered.set(text, html);
    }
    return html;
  }
  // One listener covers every code block. A check confirms a successful copy in place.
  const resets = new WeakMap<HTMLButtonElement, ReturnType<typeof setTimeout>>();
  function flash(button: HTMLButtonElement, copied: boolean) {
    clearTimeout(resets.get(button));
    button.querySelector("path")?.setAttribute("d", copied ? paths.check : paths.copy);
    button.setAttribute("aria-label", copied ? "Copied" : "Could not copy");
    resets.set(button, setTimeout(() => {
      button.querySelector("path")?.setAttribute("d", paths.copy);
      button.setAttribute("aria-label", "Copy code");
    }, 1500));
  }
  function copyCodeBlocks(node: HTMLElement) {
    const onClick = (event: MouseEvent) => {
      const button = event.target instanceof Element ? event.target.closest("button.code-copy") : null;
      const pre = button?.closest(".code-block")?.querySelector("pre");
      if (!(button instanceof HTMLButtonElement) || !pre) return;
      navigator.clipboard.writeText(pre.textContent ?? "").then(
        () => flash(button, true),
        () => flash(button, false),
      );
    };
    node.addEventListener("click", onClick);
    return { destroy: () => node.removeEventListener("click", onClick) };
  }
</script>

<script lang="ts">
  let { text }: { text: string } = $props();
  let html = $derived(render(text));
  let container: HTMLDivElement;
  $effect(() => {
    html;
    const blocks = container.querySelectorAll<HTMLElement>("pre code[class*='language-']");
    for (const block of blocks) {
      const language = [...block.classList].find(name => name.startsWith("language-"))?.slice(9) ?? "";
      const source = block.textContent ?? "";
      void highlight(source, language).then(result => {
        if (block.isConnected && block.textContent === source) block.innerHTML = DOMPurify.sanitize(result);
      });
    }
  });
</script>

<div class="markdown" bind:this={container} use:copyCodeBlocks>{@html html}</div>

<style>
  .markdown { overflow-wrap: anywhere; line-height: 1.45; }
  .markdown :global(:is(p, ul, ol, table, pre, blockquote, .code-block)) { margin: 0 0 8px; }
  .markdown :global(:is(p, ul, ol, table, pre, blockquote, .code-block):last-child) { margin-bottom: 0; }
  /* The type scale: post text is 15 px, a title 16 and a head 18. A heading is no larger than a section header. */
  .markdown :global(:is(h1, h2, h3, h4, h5, h6)) { margin: 8px 0 4px; font-size: 15px; font-weight: 600; line-height: 1.25; }
  .markdown :global(h1) { font-size: 18px; }
  .markdown :global(h2) { font-size: 16px; }
  .markdown :global(:is(h1, h2, h3, h4, h5, h6):first-child) { margin-top: 0; }
  /* A code block uses the thread's pre look and wraps, so it never scrolls sideways. */
  .markdown :global(pre) { padding: 8px 12px; border-radius: 6px; background: var(--sidebar); white-space: pre-wrap; overflow-wrap: anywhere; font: 12.5px/1.45 var(--mono); }
  /* Keep the copy control over the block without covering code or changing its height. */
  .markdown :global(.code-block) { position: relative; }
  .markdown :global(.code-head) { position: absolute; top: 4px; right: 4px; }
  .markdown :global(.code-block > pre) { padding-right: 32px; }
  .markdown :global(.code-copy) { display: grid; place-items: center; width: 24px; height: 24px; padding: 0; border: 0; border-radius: 6px; background: var(--sidebar); color: var(--muted); font: inherit; font-size: 12.5px; opacity: 0; }
  .markdown :global(.code-block:hover .code-copy), .markdown :global(.code-copy:focus-visible) { opacity: 1; }
  .markdown :global(.code-copy:hover) { color: var(--text); background: var(--hover); }
  .markdown :global(.code-copy:focus-visible) { outline: 2px solid var(--focus); outline-offset: 0; }
  @media (hover: none) {
    .markdown :global(.code-block > pre) { padding-right: 48px; }
    .markdown :global(.code-copy) { width: 40px; height: 40px; opacity: 1; }
  }
  /* A table is capped at the text width. Its cells wrap, and only a table that still cannot fit scrolls in its own block. */
  .markdown :global(table) { display: block; max-width: 100%; overflow-x: auto; border-collapse: collapse; }
  .markdown :global(:is(th, td)) { padding: 6px 12px; text-align: left; overflow-wrap: break-word; }
  .markdown :global(th) { font-weight: 600; border-bottom: 1px solid var(--line); }
  .markdown :global(tr + tr > :is(th, td)) { border-top: 1px solid var(--line); }
  .markdown :global(code) { font: 13px var(--mono); font-variant-ligatures: none; }
  .markdown :global(blockquote) { padding-left: 10px; border-left: 2px solid var(--line); color: var(--muted); }
  .markdown :global(a) { color: var(--focus); }
</style>
