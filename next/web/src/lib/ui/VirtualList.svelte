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
  import { pagingEdge, shouldPage } from "../paging.ts";
  import { SCROLL_QUIET_MS, ScrollHold } from "../scrollHold.ts";

  let { items, key, row, sticky, label, role = "feed", align = "bottom", follow = false, estimate = 44, onstart, onend, onbottom, ontop, element = $bindable() }: {
    items: T[]; key: (item: T) => string; row: Snippet<[T]>; sticky?: Snippet<[T]>; label: string; role?: "feed" | "group"; align?: "top" | "bottom";
    // Chat mode: open at the newest row and follow new rows while the reader is at the bottom.
    follow?: boolean; estimate?: number | ((item: T) => number);
    // onbottom tells whether the end of the rows is within 24 px of the view's bottom edge.
    onstart?: () => void; onend?: () => void; onbottom?: (shown: boolean) => void; ontop?: (shown: boolean) => void; element?: HTMLDivElement;
  } = $props();

  const edge = () => pagingEdge(element?.clientHeight ?? 0);
  let visible: VirtualItem[] = $state.raw([]);
  let total = $state(0);
  // The rows the virtualizer and the template use. A new items value marks the rendered rows dirty before the effect
  // below moves visible, so a row read from items could point past its end. rows and visible change together.
  // svelte-ignore state_referenced_locally
  let rows = $state.raw(items);
  // True from hold until the list's height changes. The virtualizer follows growth at the end by itself, so a threshold
  // no distance can meet turns that off.
  let held = false;
  const options = (): VirtualizerOptions<HTMLDivElement, HTMLDivElement> => ({
    count: rows.length, getScrollElement: () => element ?? null, estimateSize: index => typeof estimate === "number" ? estimate : estimate(rows[index]),
    getItemKey: index => key(rows[index]), overscan: 8, anchorTo: "end", followOnAppend: follow, scrollEndThreshold: held || holding() ? -1 : 1,
    scrollToFn: elementScroll, observeElementRect, observeElementOffset, measureElement,
    onChange: instance => { visible = instance.getVirtualItems(); total = instance.getTotalSize(); const first = visible.find(item => item.index === 0); ontop?.(!!first && first.end > (instance.scrollOffset ?? 0)); },
  });
  const scrollHold = new ScrollHold();
  const holding = () => scrollHold.active(performance.now());
  let scrollTimer: ReturnType<typeof setTimeout> | undefined;
  let waiters: (() => void)[] = [];
  const virtualizer = new Virtualizer(options());
  virtualizer.shouldAdjustScrollPositionOnItemSizeChange = (item, delta, instance) => {
    const offset = instance.scrollOffset ?? 0;
    // This mirrors virtual-core 3.19.9's default correction predicate; recheck it on upgrade.
    const adjust = !instance.itemSizeCache.has(item.key) ? item.start < offset
      : item.end <= offset && instance.scrollDirection !== "backward";
    if (adjust && holding()) { scrollHold.add(delta); return false; }
    return adjust;
  };
  function activity(touching?: boolean) {
    const wasHolding = holding();
    if (touching !== undefined) scrollHold.touch(touching);
    scrollHold.activity(performance.now());
    if (!wasHolding) virtualizer.setOptions(options());
    clearTimeout(scrollTimer);
    scrollTimer = setTimeout(finishScroll, SCROLL_QUIET_MS);
  }
  function finishScroll() {
    if (holding()) { scrollTimer = setTimeout(finishScroll, SCROLL_QUIET_MS); return; }
    const delta = scrollHold.take();
    if (element && delta) element.scrollTop += delta;
    virtualizer.setOptions(options());
    for (const done of waiters.splice(0)) done();
  }
  // A fetched older page waits for the reader's current scroll before it changes the list.
  export function scrollSettled(): Promise<void> {
    if (!holding()) return Promise.resolve();
    return new Promise(resolve => waiters.push(resolve));
  }
  $effect(() => () => { clearTimeout(scrollTimer); for (const done of waiters.splice(0)) done(); });

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
    return element !== undefined && virtualizer.getTotalSize() > element.clientHeight + edge();
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
    if (event && holding()) activity();
    const top = element.scrollTop;
    atEnd = top + element.clientHeight >= element.scrollHeight - 2;
    const page = shouldPage(top, element.scrollHeight, element.clientHeight, edge(), event ? top - last : undefined);
    last = top;
    if (page.start) onstart?.();
    if (page.end) onend?.();
  }
  const measure = (node: HTMLDivElement) => { virtualizer.measureElement(node); };
  // The browser checks the end mark after its own layout of each frame; the last queued entry is the current state.
  // A scroll handler that read scrollHeight after the rows changed forced an extra layout on each scroll event:
  // 637 ms in a 20 second trace.
  const watch = (shown: (entry: IntersectionObserverEntry) => void, node: HTMLDivElement) => {
    const observer = new IntersectionObserver(entries => shown(entries.at(-1)!), { root: element, rootMargin: "0px 0px 24px 0px" });
    observer.observe(node);
    return observer;
  };
  let end: HTMLDivElement | undefined = $state();
  const bottom = (node: HTMLDivElement) => {
    if (!element || !onbottom) return;
    const report = onbottom, observer = watch(entry => report(entry.isIntersecting), node);
    return () => observer.disconnect();
  };
  // Whether the end is in view in the layout the next frame draws. A flag kept from an earlier callback can be stale
  // after rows were added or grew, so a reader who must not be told "seen" too early asks here.
  export function atBottom(): Promise<boolean> {
    return new Promise(resolve => {
      if (!end || !element) { resolve(false); return; }
      const observer = watch(entry => { observer.disconnect(); resolve(entry.isIntersecting); }, end);
    });
  }
</script>

<div class="virtual" {role} aria-label={label} tabindex="-1" bind:this={element} onscroll={scrolled}
  onwheel={() => activity()} ontouchstart={() => activity(true)} ontouchmove={() => activity()}
  ontouchend={() => activity(false)} ontouchcancel={() => activity(false)}
  onkeydown={event => { if (["ArrowUp", "ArrowDown", "PageUp", "PageDown", "Home", "End", " "].includes(event.key)) activity(); }}>
  {#if sticky && visible.length}{@const first = visible.find(item => item.end > (virtualizer.scrollOffset ?? 0)) ?? visible[0]}<div class="sticky">{@render sticky(rows[first.index])}</div>{/if}
  <div class="space" class:top={align === "top"} style:height="{total}px">
    {#each visible as item (item.key)}
      <div class="item" data-index={item.index} style:transform="translateY({item.start}px)" {@attach measure}>
        {@render row(rows[item.index])}
      </div>
    {/each}
    <div class="end" bind:this={end} {@attach bottom}></div>
  </div>
</div>

<style>
  /* A container, so a row in the capped column can reach the pane's edges with cqw (PostRow). */
  .virtual { container-type: inline-size; height: 100%; overflow-y: auto; display: flex; flex-direction: column; }
  .sticky { position: sticky; top: 0; height: 0; z-index: 1; }
  .space { position: relative; flex: none; margin-top: auto; }
  .space.top { margin-top: 0; }
  .end { position: absolute; bottom: 0; height: 0; }
  .item { position: absolute; top: 0; left: 0; right: 0; width: 100%; max-width: var(--feed-reading-width, none); margin: 0 auto; }
</style>
