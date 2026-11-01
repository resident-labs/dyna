<script lang="ts" generics="T">
  import { onMount, type Snippet } from "svelte";
  import Icon from "./Icon.svelte";
  // The dropdown behind a quiet menu button, shared by ProfileMenu and ParentMenu. Each entry is a radio item, and the
  // entry whose key is checked gets a tick. The checked entry, or the first, takes focus on open. Up and Down move
  // between entries, Home and End jump, Escape closes and refocuses the button, and Tab leaves. The list is fixed to the
  // viewport beside its anchor, so a panel's scroll area cannot clip it, and it stays inside the viewport's edges.
  let { label, items, keyOf, checked, row, anchor, onchoose, onclose }: {
    label: string; items: T[]; keyOf: (item: T) => string; checked: string; row: Snippet<[T]>; anchor: HTMLElement;
    onchoose: (key: string) => void; onclose: (refocus: boolean) => void;
  } = $props();
  let list: HTMLDivElement | undefined = $state();
  const entries = () => Array.from(list?.querySelectorAll<HTMLButtonElement>("[role=menuitemradio]") ?? []);
  onMount(() => {
    if (list) {
      const box = anchor.getBoundingClientRect();
      const left = Math.min(box.right - list.offsetWidth, document.documentElement.clientWidth - list.offsetWidth - 16);
      list.style.left = `${Math.max(16, left)}px`;
      list.style.top = `${box.bottom + 4}px`;
    }
    (entries().find(item => item.ariaChecked === "true") ?? entries()[0])?.focus();
  });
  function move(event: KeyboardEvent) {
    const all = entries();
    const index = all.findIndex(item => item === document.activeElement);
    const to = { ArrowDown: index + 1, ArrowUp: index - 1, Home: 0, End: all.length - 1 }[event.key];
    if (to !== undefined) { event.preventDefault(); all[(to + all.length) % all.length]?.focus(); }
    if (event.key === "Escape") { event.stopPropagation(); onclose(true); }
    if (event.key === "Tab") onclose(false);
  }
</script>

<div class="list" role="menu" aria-label={label} tabindex="-1" bind:this={list} onkeydown={move}>
  {#each items as item (keyOf(item))}{@const key = keyOf(item)}<button type="button" role="menuitemradio" aria-checked={key === checked} tabindex="-1" onclick={() => onchoose(key)}>
    {@render row(item)}{#if key === checked}<Icon name="check" />{/if}</button>{/each}
</div>

<style>
  .list { position: fixed; z-index: 2; width: 232px; max-width: calc(100vw - 32px); padding: 4px; display: grid;
    background: var(--raised); border: 1px solid var(--line); border-radius: 8px; box-shadow: 0 6px 20px var(--shadow-md); }
  [role="menuitemradio"] { display: flex; align-items: start; justify-content: space-between; gap: 12px; padding: 6px 8px; border: 0; border-radius: 6px; background: none; color: var(--text); font: inherit; font-size: 13px; line-height: 18px; text-align: left; cursor: pointer; }
  [role="menuitemradio"] :global(svg) { flex: none; width: 16px; height: 16px; margin-top: 1px; color: var(--muted); }
  /* Items have no border or ring of their own; a fill marks the hovered or focused one. */
  [role="menuitemradio"]:hover, [role="menuitemradio"]:focus { background: var(--selected); outline: none; }
  @media (max-width: 640px) { [role="menuitemradio"] { padding: 11px 8px; } }
</style>
