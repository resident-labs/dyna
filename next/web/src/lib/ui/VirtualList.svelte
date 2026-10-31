<script lang="ts" generics="T">
  // The app's only windowing code. Rows have any height and are measured after render.
  // anchorTo "end" keeps the top visible row in place when rows are added or released at
  // either end; followOnAppend sticks to the bottom only while the reader is already there.
  import {
    Virtualizer, elementScroll, measureElement, observeElementOffset, observeElementRect,
    type VirtualItem, type VirtualizerOptions,
  } from "@tanstack/virtual-core";
  import { tick, untrack, type Snippet } from "svelte";

  let { items, key, row, label, follow = false, estimate = 44, onstart, onend, element = $bindable() }: {
    items: T[]; key: (item: T) => string; row: Snippet<[T]>; label: string;
    // Chat mode: open at the newest row and follow new rows while the reader is at the bottom.
    follow?: boolean; estimate?: number;
    onstart?: () => void; onend?: () => void; element?: HTMLDivElement;
  } = $props();

  const edge = 600;
  let visible: VirtualItem[] = $state([]);
  let total = $state(0);
  const options = (): VirtualizerOptions<HTMLDivElement, HTMLDivElement> => ({
    count: items.length, getScrollElement: () => element ?? null, estimateSize: () => estimate,
    getItemKey: index => key(items[index]), overscan: 8, anchorTo: "end", followOnAppend: follow,
    scrollToFn: elementScroll, observeElementRect, observeElementOffset, measureElement,
    onChange: instance => { visible = instance.getVirtualItems(); total = instance.getTotalSize(); },
  });
  const virtualizer = new Virtualizer(options());

  $effect.pre(() => { virtualizer.setOptions(options()); });
  $effect(() => {
    void items.length;
    virtualizer._willUpdate();
    visible = virtualizer.getVirtualItems();
    total = virtualizer.getTotalSize();
  });
  $effect(() => {
    const cleanup = virtualizer._didMount();
    virtualizer._willUpdate();
    if (untrack(() => follow)) virtualizer.scrollToEnd();
    return cleanup;
  });
  // Lands on the newest row once new rows render; scrollToEnd keeps retargeting as they are measured.
  export async function toEnd(): Promise<void> {
    await tick();
    virtualizer.scrollToEnd();
  }

  function scrolled() {
    if (!element) return;
    if (element.scrollTop < edge) onstart?.();
    if (virtualizer.getDistanceFromEnd() < edge) onend?.();
  }
  const measure = (node: HTMLDivElement) => { virtualizer.measureElement(node); };
</script>

<div class="virtual" role="feed" aria-label={label} tabindex="-1" bind:this={element} onscroll={scrolled}>
  <div class="space" style:height="{total}px">
    {#each visible as item (item.key)}
      <div class="item" data-index={item.index} style:transform="translateY({item.start}px)" {@attach measure}>
        {@render row(items[item.index])}
      </div>
    {/each}
  </div>
</div>

<style>
  .virtual { height: 100%; overflow-y: auto; display: flex; flex-direction: column; }
  .space { position: relative; flex: none; margin-top: auto; }
  .item { position: absolute; top: 0; left: 0; width: 100%; }
</style>
