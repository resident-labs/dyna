<script lang="ts">
  import Icon from "./Icon.svelte";
  import { render } from "./PostRow.svelte";
  import type { Quote } from "../feed.ts";
  // Enter sends and Shift+Enter starts a new line. The action row keeps room for the attach and mention buttons.
  // A forwarded post waits above the text as in v1; the × or Escape removes it.
  let { value = $bindable(""), placeholder, onsend, quote, onunquote, element = $bindable() }: {
    value?: string; placeholder: string; onsend: () => void; quote?: Quote; onunquote: () => void; element?: HTMLTextAreaElement;
  } = $props();
  function keydown(event: KeyboardEvent) {
    if (event.key === "Escape" && quote) { event.stopPropagation(); onunquote(); return; }
    if (event.key !== "Enter" || event.shiftKey || event.isComposing) return;
    event.preventDefault();
    onsend();
  }
</script>

<div class="composer">
  {#if quote}<div class="quote"><p class="label"><Icon name="forward" size={14} /><span>Forwarding a post from <strong>{quote.author}</strong></span>
    <button type="button" aria-label="Remove forwarded post" title="Remove forwarded post" onclick={() => { onunquote(); element?.focus(); }}><Icon name="close" size={14} /></button></p>
    <div class="quoted">{@html render(quote.text)}</div></div>{/if}
  <textarea rows="2" aria-label="Message" bind:this={element} bind:value onkeydown={keydown} {placeholder}></textarea>
  <div class="actions">
    <span class="keys">Enter to send · Shift+Enter for a new line</span>
    <button type="button" aria-label="Send" title="Send" disabled={!value.trim() && !quote} onclick={() => { onsend(); element?.focus(); }}><Icon name="send" /></button>
  </div>
</div>

<style>
  .composer { flex: none; margin: 0 16px 16px; padding: 12px 14px 8px; border: 1px solid var(--line); border-radius: 12px; background: var(--raised); }
  /* The box border is the textarea's focus ring. */
  .composer:has(textarea:focus-visible) { border-color: var(--focus); box-shadow: 0 0 0 1px var(--focus); }
  /* v1's draft chip: a left rule in the focus color, the label, and the post folded to three lines. */
  .quote { margin-bottom: 8px; padding: 6px 10px; border: 1px solid var(--line); border-left: 3px solid var(--focus); border-radius: 6px; background: var(--sidebar); }
  .label { margin: 0; display: flex; align-items: center; gap: 6px; color: var(--muted); font-size: 12px; }
  .label span { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .label strong { color: var(--text); font-weight: 500; }
  .quote button { all: unset; display: grid; place-items: center; width: 24px; height: 24px; border-radius: 4px; color: var(--muted); cursor: pointer; }
  .quote button:hover { background: var(--hover); color: var(--text); }
  .quote button:focus-visible { outline: 2px solid var(--focus); outline-offset: 0; }
  @media (hover: none) { .quote button { width: 40px; height: 40px; } }
  .quoted { max-height: 4.5em; overflow: hidden; color: var(--muted); font-size: 13px; overflow-wrap: anywhere; }
  .quoted :global(:is(p, ul, ol, pre, blockquote)) { margin: 0; }
  textarea { display: block; width: 100%; padding: 0; border: 0; background: none; color: inherit; font: inherit; resize: none; field-sizing: content; min-height: 3em; max-height: 40vh; }
  textarea:focus-visible { outline: none; }
  textarea::placeholder { color: var(--muted); }
  .actions { display: flex; justify-content: space-between; align-items: center; gap: 8px; margin-top: 4px; }
  /* The key hint shows only while the empty message box has focus. */
  .keys { font-size: 12px; color: var(--muted); visibility: hidden; }
  .composer:has(textarea:focus:placeholder-shown) .keys { visibility: visible; }
  .actions button { display: grid; place-items: center; width: 32px; height: 32px; border: 0; border-radius: 8px; background: var(--focus); color: var(--bg); cursor: pointer; }
  .actions button:disabled { background: var(--selected); color: var(--muted); cursor: default; }
  .actions button:focus-visible { outline-offset: 2px; }
</style>
