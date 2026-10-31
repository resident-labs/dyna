<script lang="ts">
  import Icon from "./Icon.svelte";
  import StatusPill from "./StatusPill.svelte";
  import { ruleLimit } from "../rules.ts";
  // Where a rule is written: it grows with its text, Shift+Enter adds a line, and a count shows past 90% of the limit.
  // A rule over the limit cannot be saved.
  let { value = $bindable(), label, placeholder, busy, onsave }: { value: string; label: string; placeholder: string; busy: boolean; onsave: () => void } = $props();
  let size = $derived([...value].length);
  let over = $derived(size > ruleLimit);
  const save = () => { if (!over) onsave(); };
</script>

<div class="box"><textarea rows="1" bind:value {placeholder} aria-label={label} aria-invalid={over} readonly={busy}
  onkeydown={event => {
    if (event.key === "Enter" && !event.shiftKey && !event.isComposing) { event.preventDefault(); save(); }
    // Escape keeps a typed rule: the page closes only on Escape in an empty box.
    if (event.key === "Escape" && value) event.stopPropagation();
  }}></textarea>
  {#if size > ruleLimit * 0.9}<span class="size" role="status"><StatusPill tone={over ? "error" : "warning"} word label={`${size.toLocaleString("en-US")} of ${ruleLimit.toLocaleString("en-US")}`}
    title={over ? "This rule is too long to save" : "Rules can have up to 2,000 characters"} /></span>
  {:else}<span class="hint" aria-hidden="true"><kbd>Enter</kbd> adds</span>{/if}
  <button type="button" class="send" aria-label="Add rule" disabled={over || busy || !value.trim()} onclick={save}><Icon name="send" /></button></div>

<style>
  .box { display: flex; align-items: center; gap: 8px; padding: 10px 14px; border: 1px solid var(--line); border-radius: 12px; background: var(--raised); }
  .box:has(textarea:focus-visible) { border-color: var(--focus); box-shadow: 0 0 0 1px var(--focus); }
  textarea { flex: 1; min-width: 0; padding: 0; border: 0; background: none; color: inherit; font: inherit; font-size: 15px; line-height: 1.45; resize: none; field-sizing: content; max-height: 30vh; }
  @media (pointer: coarse) { textarea { font-size: 16px; } }
  textarea:focus-visible { outline: none; }
  textarea::placeholder { color: var(--muted); }
  .size { flex: none; align-self: flex-end; font-variant-numeric: tabular-nums; }
  .hint { flex: none; align-self: flex-end; color: var(--muted); font-size: 12.5px; }
  kbd { padding: 1px 5px; border: 1px solid var(--line); border-radius: 4px; font: 11px var(--mono); }
  .send { display: none; flex: none; align-self: flex-end; place-items: center; width: 40px; height: 40px; margin: -8px -8px -8px 0; padding: 0; border: 0; border-radius: 6px; background: none; color: var(--muted); }
  .send:disabled { opacity: 0.5; }
  /* A touch keyboard's Enter may only add a line, so a touch screen gets a button. */
  @media (hover: none) { .hint { display: none; } .send { display: grid; } }
</style>
