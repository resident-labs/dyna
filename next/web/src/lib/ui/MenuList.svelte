<script lang="ts" generics="T">
  import { onMount, type Snippet } from "svelte";
  import Icon from "./Icon.svelte";
  // The dropdown shared by ProfileMenu and SessionActions. With checked, each entry is a radio item and the checked one
  // gets a tick; without it, each entry is a plain action. The checked entry, or the first, takes focus on open. Up and
  // Down move between entries, Home and End jump, Escape closes and refocuses the opener, and Tab leaves. The list is
  // fixed to the viewport beside its anchor, an element or a point's rect, so a panel's scroll area cannot clip it, and
  // it stays inside the viewport's edges. side picks the anchor edge it lines up with. note is a line above the entries.
  let { label, items, keyOf, checked, row, anchor, side = "end", note, onchoose, onclose }: {
    label: string; items: T[]; keyOf: (item: T) => string; checked?: string; row: Snippet<[T]>; anchor: HTMLElement | DOMRect;
    side?: "start" | "end"; note?: string; onchoose: (key: string) => void; onclose: (refocus: boolean) => void;
  } = $props();
  let list: HTMLDivElement | undefined = $state();
  const entries = () => Array.from(list?.querySelectorAll<HTMLButtonElement>("[role^=menuitem]") ?? []);
  onMount(() => {
    if (list) {
      const box = anchor instanceof DOMRect ? anchor : anchor.getBoundingClientRect();
      const left = Math.min(side === "start" ? box.left : box.right - list.offsetWidth, document.documentElement.clientWidth - list.offsetWidth - 16);
      const below = document.documentElement.clientHeight - box.bottom - 16;
      const above = box.top - 16;
      const opensUp = list.offsetHeight > below && above > below;
      const space = opensUp ? above : below;
      list.style.left = `${Math.max(16, left)}px`;
      list.style.maxHeight = `${space}px`;
      list.style.top = `${opensUp ? box.top - Math.min(list.offsetHeight, space) - 4 : box.bottom + 4}px`;
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
  {#if note}<p role="status">{note}</p>{/if}
  {#each items as item (keyOf(item))}{@const key = keyOf(item)}<button type="button" role={checked === undefined ? "menuitem" : "menuitemradio"} aria-checked={checked === undefined ? undefined : key === checked} tabindex="-1" onclick={() => onchoose(key)}>
    {@render row(item)}{#if key === checked}<Icon name="check" />{/if}</button>{/each}
</div>

<style>
  .list { position: fixed; z-index: 2; width: 232px; max-width: calc(100vw - 32px); padding: 4px; display: grid; overflow-y: auto;
    background: var(--raised); border: 1px solid var(--line); border-radius: 8px; box-shadow: 0 6px 20px var(--shadow-md); }
  p { margin: 0; padding: 6px 8px; color: var(--muted); font-size: 12.5px; line-height: 1.45; }
  button { display: flex; align-items: start; justify-content: space-between; gap: 12px; padding: 6px 8px; border: 0; border-radius: 6px; background: none; color: var(--text); font: inherit; font-size: 13px; line-height: 18px; text-align: left; cursor: pointer; }
  button :global(svg) { flex: none; width: 16px; height: 16px; margin-top: 1px; color: var(--muted); }
  /* Items have no border or ring of their own; a fill marks the hovered or focused one. */
  button:hover, button:focus { background: var(--selected); outline: none; }
  @media (max-width: 640px) { button { padding: 11px 8px; } }
</style>
