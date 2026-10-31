<script lang="ts">
  import Icon from "./Icon.svelte";
  // Enter sends and Shift+Enter starts a new line. The action row keeps room for the attach and mention buttons.
  let { value = $bindable(""), placeholder, onsend, element = $bindable() }: {
    value?: string; placeholder: string; onsend: () => void; element?: HTMLTextAreaElement;
  } = $props();
  function keydown(event: KeyboardEvent) {
    if (event.key !== "Enter" || event.shiftKey || event.isComposing) return;
    event.preventDefault();
    onsend();
  }
</script>

<div class="composer">
  <textarea rows="2" aria-label="Message" bind:this={element} bind:value onkeydown={keydown} {placeholder}></textarea>
  <div class="actions">
    <button type="button" aria-label="Send" title="Send" disabled={!value.trim()} onclick={() => { onsend(); element?.focus(); }}><Icon name="send" /></button>
  </div>
</div>

<style>
  .composer { flex: none; margin: 0 16px 16px; padding: 12px 14px 8px; border: 1px solid var(--line); border-radius: 12px; background: var(--raised); }
  /* The box border is the textarea's focus ring. */
  .composer:has(textarea:focus-visible) { border-color: var(--focus); box-shadow: 0 0 0 1px var(--focus); }
  textarea { display: block; width: 100%; padding: 0; border: 0; background: none; color: inherit; font: inherit; resize: none; field-sizing: content; min-height: 3em; max-height: 40vh; }
  textarea:focus-visible { outline: none; }
  textarea::placeholder { color: var(--muted); }
  .actions { display: flex; justify-content: flex-end; margin-top: 4px; }
  button { display: grid; place-items: center; width: 32px; height: 32px; border: 0; border-radius: 8px; background: var(--focus); color: var(--bg); cursor: pointer; }
  button:disabled { background: var(--selected); color: var(--muted); cursor: default; }
  button:focus-visible { outline-offset: 2px; }
</style>
