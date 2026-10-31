<script lang="ts" generics="T">
  // The app's only windowing code. Rows have any height and are measured after render.
  // anchorTo "end" keeps the top visible row in place when rows are added or released at
  // either end; followOnAppend sticks to the bottom only while the reader is already there.
  import {
    Virtualizer, elementScroll, measureElement, observeElementOffset, observeElementRect,
    type VirtualItem, type VirtualizerOptions,
  } from "@tanstack/virtual-core";
  import { tick, untrack, type Snippet } from "svelte";
  import { shouldPage } from "../paging.ts";

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

  // Visible rows follow the new items before the rows render, so no row points past the end.
  $effect.pre(() => {
    virtualizer.setOptions(options());
    visible = virtualizer.getVirtualItems();
    total = virtualizer.getTotalSize();
  });
  $effect(() => {
    void items.length;
    virtualizer._willUpdate();
  });
  // The wheel fires no scroll event at an edge, so a reader who reached it while a page
  // was loading gets the next page when the rows change. Rows that barely overflow page
  // nothing here; Feed's fill handles them without leaving the present.
  $effect(() => {
    void items;
    void tick().then(() => scrolled());
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

  // True once the rows overflow the view far enough to scroll up to older ones.
  export function full(): boolean {
    return element !== undefined && virtualizer.getTotalSize() > element.clientHeight + edge;
  }

  // Reads the element itself: the scroll handler runs before the virtualizer sees the new offset.
  let last = 0, atEnd = true;
  // A view that shrinks, as when a phone's keyboard opens, keeps its scrollTop; a reader at the newest row stays there.
  $effect(() => {
    if (!element || !follow) return;
    const view = element, observer = new ResizeObserver(() => { if (atEnd) view.scrollTop = view.scrollHeight; });
    observer.observe(view);
    return () => observer.disconnect();
  });
  function scrolled(event?: Event) {
    if (!element) return;
    const top = element.scrollTop;
    atEnd = top + element.clientHeight >= element.scrollHeight - 2;
    const page = shouldPage(top, element.scrollHeight, element.clientHeight, edge, event ? top - last : undefined);
    last = top;
    if (page.start) onstart?.();
    if (page.end) onend?.();
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
