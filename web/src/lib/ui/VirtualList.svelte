<script>
  // VirtualList renders only the rows near the viewport of `scroller`, so a
  // list of thousands of rows costs about one screen of DOM. Rows can have any
  // height; each is measured once it renders. With `chat`, the list opens at
  // its last row, keeps the reader at the bottom as rows arrive and keeps the
  // visible row in place when older rows load above it.
  import { untrack } from 'svelte'
  import { Virtualizer, elementScroll, observeElementOffset, observeElementRect } from '@tanstack/virtual-core'

  let { scroller, items, key, estimate, chat = false, follow = true, children } = $props()

  let list = $state()
  let version = $state(0)
  // Content above the list inside the scroller, such as a header.
  let margin = $state(0)
  let opened = false

  const options = () => ({
    count: items.length,
    getScrollElement: () => scroller ?? null,
    getItemKey: (i) => key(items[i]),
    estimateSize: (i) => estimate(items[i]),
    scrollToFn: elementScroll,
    observeElementRect,
    observeElementOffset,
    // A screen of rows renders before the scroller is measured.
    initialRect: { width: 0, height: globalThis.innerHeight || 1000 },
    scrollMargin: margin,
    overscan: 6,
    anchorTo: chat ? 'end' : 'start',
    followOnAppend: chat && follow,
    // Matches the feed's own "at the bottom" distance.
    scrollEndThreshold: 48,
    onChange: () => version++,
  })
  const v = new Virtualizer(untrack(options))

  // setOptions must see the old rows and the new ones together: that is how
  // the list keeps its place when rows are added above.
  $effect.pre(() => {
    v.setOptions(options())
    untrack(() => version++)
  })
  $effect(() => {
    version
    if (list) margin = list.offsetTop
    // Release rows removed by virtualization or by a discarded history page.
    v.measureElement(null)
    v._willUpdate()
    // A chat list opens at its latest row, once it has a scroller.
    if (chat && !opened && v.scrollElement) { opened = true; v.scrollToEnd() }
  })
  $effect(() => v._didMount())

  let shown = $derived((version, v.getVirtualItems()))
  let total = $derived((version, v.getTotalSize()))

  function measure(node) {
    v.measureElement(node)
    return { update: () => v.measureElement(node) }
  }

  // The first row can be a date divider replaced by the next page. Keep a
  // visible row after it, using the virtualizer's measured offsets.
  export function captureAnchor() {
    if (!list || !scroller) return null
    const top = scroller.getBoundingClientRect().top
    const rows = [...list.children].filter(node => Number(node.dataset.index) > 0 && node.getBoundingClientRect().height > 0 && node.getBoundingClientRect().bottom > top)
    const node = rows.find(node => node.getBoundingClientRect().top >= top) ?? rows[0]
    if (!node) return null
    return { key: key(items[Number(node.dataset.index)]), top: node.getBoundingClientRect().top - top }
  }
  export function restoreAnchor(anchor) {
    if (!anchor) return
    const index = items.findIndex(item => key(item) === anchor.key)
    if (index < 0) return
    const offset = v.getOffsetForIndex(index, 'start')
    if (offset) scroller.scrollTop = offset[0] - anchor.top
  }

  export function scrollToIndex(i, align = 'center') {
    v.scrollToIndex(i, { align })
  }
  export function scrollToEnd() {
    v.scrollToEnd()
  }
</script>

<div class="vlist" bind:this={list} style:height="{total}px">
  {#each shown as it (it.key)}
    <div class="vrow" data-index={it.index} use:measure={it.index} style:transform="translateY({it.start - margin}px)">
      {@render children(items[it.index], it.index)}
    </div>
  {/each}
</div>

<style>
  .vlist { position: relative; }
  /* flow-root keeps a row's margins inside its measured box. */
  .vrow { position: absolute; top: 0; left: 0; right: 0; display: flow-root; }
</style>
