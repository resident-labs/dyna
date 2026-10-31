<script lang="ts" generics="T">
  // The app's only windowing code. Rows have any height and are measured after render.
  // anchorTo "end" keeps the top visible row in place when rows are added or released at
  // either end; followOnAppend sticks to the bottom only while the reader is already there. A short list sits at the
  // bottom, as chat does, unless align is "top", as for the sessions panel; role names what the rows are.
  import {
    Virtualizer, elementScroll, measureElement, observeElementOffset, observeElementRect,
    type VirtualItem, type VirtualizerOptions,
  } from "@tanstack/virtual-core";
  import { tick, untrack, type Snippet } from "svelte";
  import { shouldPage } from "../paging.ts";

  let { items, key, row, label, role = "feed", align = "bottom", follow = false, estimate = 44, onstart, onend, element = $bindable() }: {
    items: T[]; key: (item: T) => string; row: Snippet<[T]>; label: string; role?: "feed" | "group"; align?: "top" | "bottom";
    // Chat mode: open at the newest row and follow new rows while the reader is at the bottom.
    follow?: boolean; estimate?: number;
    onstart?: () => void; onend?: () => void; element?: HTMLDivElement;
  } = $props();

  const edge = 600;
  let visible: VirtualItem[] = $state([]);
  let total = $state(0);
  // The rows the virtualizer and the template use. A new items value marks the rendered rows dirty before the effect
  // below moves visible, so a row read from items could point past its end. rows and visible change together.
  // svelte-ignore state_referenced_locally
  let rows = $state.raw(items);
  // True from hold until the list's height changes. The virtualizer follows growth at the end by itself, so a threshold
  // no distance can meet turns that off.
  let held = false;
  const options = (): VirtualizerOptions<HTMLDivElement, HTMLDivElement> => ({
    count: rows.length, getScrollElement: () => element ?? null, estimateSize: () => estimate,
    getItemKey: index => key(rows[index]), overscan: 8, anchorTo: "end", followOnAppend: follow, scrollEndThreshold: held ? -1 : 1,
    scrollToFn: elementScroll, observeElementRect, observeElementOffset, measureElement,
    onChange: instance => { visible = instance.getVirtualItems(); total = instance.getTotalSize(); },
  });
  const virtualizer = new Virtualizer(options());

  $effect.pre(() => {
    rows = items;
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

  // Scrolls a row into the window and waits for it to render, so a caller can focus it.
  export async function reveal(index: number): Promise<void> {
    virtualizer.scrollToIndex(index);
    await new Promise(requestAnimationFrame);
    await tick();
  }

  // The reader opened something at the newest row, so its growth stays where they are looking until they scroll.
  export function hold(): void {
    atEnd = false;
    held = true;
    virtualizer.setOptions(options());
  }

  // True once the rows overflow the view far enough to scroll up to older ones.
  export function full(): boolean {
    return element !== undefined && virtualizer.getTotalSize() > element.clientHeight + edge;
  }

  // Reads the element itself: the scroll handler runs before the virtualizer sees the new offset.
  let last = 0, atEnd = true;
  // Rows taller than their estimate grow the list after scrollToEnd has landed, so a reader at the newest row stays there.
  $effect(() => {
    void total;
    if (held) void tick().then(() => {
      held = false;
      virtualizer.setOptions(options());
      if (element) atEnd = element.scrollTop + element.clientHeight >= element.scrollHeight - 2;
    });
    else if (element && atEnd && untrack(() => follow)) element.scrollTop = element.scrollHeight;
  });
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

<div class="virtual" {role} aria-label={label} tabindex="-1" bind:this={element} onscroll={scrolled}>
  <div class="space" class:top={align === "top"} style:height="{total}px">
    {#each visible as item (item.key)}
      <div class="item" data-index={item.index} style:transform="translateY({item.start}px)" {@attach measure}>
        {@render row(rows[item.index])}
      </div>
    {/each}
  </div>
</div>

<style>
  .virtual { height: 100%; overflow-y: auto; display: flex; flex-direction: column; }
  .space { position: relative; flex: none; margin-top: auto; }
  .space.top { margin-top: 0; }
  .item { position: absolute; top: 0; left: 0; width: 100%; }
</style>
