<script lang="ts">
  import Icon from "../Icon.svelte";
  // The search box of a Settings list page, and of a list inside a detail, such as a rooms picker. Esc clears it first.
  // slash makes / focus it from anywhere on the page that is not typing; only a page's own list search takes it.
  // ondown is ↓ in the box, which moves to the list below.
  let { value = $bindable(""), label, placeholder, slash = false, disabled = false, ondown }: {
    value?: string; label: string; placeholder: string; slash?: boolean; disabled?: boolean; ondown?: () => void;
  } = $props();
  let input: HTMLInputElement | undefined = $state();

  function key(event: KeyboardEvent) {
    if (event.key === "Escape" && value) { event.preventDefault(); value = ""; }
    else if (event.key === "ArrowDown" && ondown) { event.preventDefault(); ondown(); }
  }
  function anywhere(event: KeyboardEvent) {
    if (!slash || event.key !== "/" || event.defaultPrevented || event.ctrlKey || event.metaKey || event.altKey || !input || disabled) return;
    const target = event.target as HTMLElement;
    if (target.isContentEditable || target.closest("textarea, select, input:not([type=checkbox]):not([type=radio])")) return;
    event.preventDefault();
    input.focus();
    input.select();
  }
</script>

<svelte:window onkeydown={anywhere} />
<label class="search"><Icon name="search" /><input bind:this={input} bind:value type="search" {placeholder} aria-label={label} {disabled} autocomplete="off" spellcheck="false" onkeydown={key} />{#if slash}<kbd aria-hidden="true">/</kbd>{/if}</label>

<style>
  .search { flex: 1 1 240px; display: flex; align-items: center; gap: 8px; min-width: 0; height: 34px; padding: 0 10px; border: 1px solid var(--line); border-radius: 6px; background: var(--bg); color: var(--muted); }
  .search:focus-within { border-color: var(--focus); }
  input { flex: 1; min-width: 0; height: 100%; padding: 0; border: 0; outline: 0; background: none; color: var(--text); font: inherit; font-size: 14px; }
  input::placeholder { color: var(--muted); }
  input:disabled { cursor: default; }
  kbd { padding: 1px 5px; border: 1px solid var(--firm); border-radius: 4px; font: 11px var(--mono); }
  @media (max-width: 640px), (hover: none) { kbd { display: none; } }
</style>
