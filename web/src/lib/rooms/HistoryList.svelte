<script lang="ts" generics="T">
  import type { Snippet } from 'svelte'
  import { onDestroy, tick } from 'svelte'
  import VirtualList from '../ui/VirtualList.svelte'
  import { errorText } from '../errorText.ts'

  // A row key, as the pager and the virtual list compare them.
  type RowKey = string | number
  // The place a paging request starts from: the first row's key, the scroll
  // position and the virtualizer's own anchor.
  interface Anchor {
    first: RowKey | null
    position: number | undefined
    row: unknown
  }
  // The parts of VirtualList this list drives.
  interface VirtualListApi {
    captureAnchor: () => unknown
    restoreAnchor: (row: unknown) => void
    scrollToIndex: (index: number, align?: string) => void
    scrollToEnd: () => void
  }

  interface Props<T> {
    scroller?: HTMLElement | null
    items: T[]
    key: (item: T) => RowKey
    estimate: (item: T, index: number) => number
    earlier?: boolean
    newer?: boolean
    onearlier?: ((near: () => boolean) => unknown) | null
    onnewer?: ((near: () => boolean) => unknown) | null
    onpresent?: (() => unknown) | null
    beginning?: string
    onhistory?: (() => void) | null
    fill?: boolean
    atBottom?: boolean
    children: Snippet<[T, number]>
  }

  let { scroller = null, items, key, estimate, earlier = false, newer = false, onearlier = null, onnewer = null, onpresent = null, beginning = '', onhistory = null, fill = true, atBottom = $bindable(true), children: renderRow }: Props<T> = $props()
  let list: VirtualListApi | undefined = $state()
  let busy = $state(false)
  let error = $state('')
  let loading = $state<'older' | 'newer' | 'present'>('older')
  let request: Promise<boolean> | null = null
  let pendingAnchor: Anchor | null = null
  // A request outlives the view it started in: a reader who opens another page
  // mid-load would otherwise have the paging loop read a list it no longer owns.
  let destroyed = false
  onDestroy(() => (destroyed = true))

  // below puts a control in the row after the scroller, where it takes its own
  // space instead of covering the list. A node Svelte tears down is already
  // gone, so destroy only clears one that is left over.
  function below(node: HTMLElement, target: HTMLElement | null) {
    const place = (next: HTMLElement | null): void => {
      if (next?.parentNode) next.after(node)
    }
    place(target)
    return { update: place, destroy: () => node.remove() }
  }

  // edgeKey is a page edge's key; an empty list has none.
  const edgeKey = (item: T | undefined): RowKey | null => (item === undefined ? null : key(item))

  // atEdge is true when the scroller sits within one screen of the loaded end.
  const atEdge = (el: HTMLElement, older: boolean): boolean =>
    (older ? el.scrollTop : el.scrollHeight - el.scrollTop - el.clientHeight) <= el.clientHeight

  // Both directions, filtered pages and jumps share one request.
  export function loadEarlier() { return loadPage('older') }

  function loadPage(direction: 'older' | 'newer'): Promise<boolean> {
    if (request) return request
    const older = direction === 'older'
    const load = older ? onearlier : onnewer
    if (!(older ? earlier : newer) || !load) return Promise.resolve(false)
    busy = true
    loading = direction
    error = ''
    onhistory?.()
    request = Promise.resolve().then(async () => {
      try {
        do {
          const anchor: Anchor = { first: edgeKey(items[0]), position: scroller?.scrollTop, row: list?.captureAnchor() }
          pendingAnchor = anchor
          const edge = edgeKey(older ? items[0] : items.at(-1))
          const nearEdge = () => scroller !== null && atEdge(scroller, older)
          if (await load(nearEdge) === false) {
            error = 'Could not load messages.'
            return false
          }
          await tick()
          // The view is gone: its items belong to a destroyed parent.
          if (destroyed) return false
          // The virtualizer handles retained keys. A replaced first divider
          // needs the next visible row; do not override a later scroll.
          if (anchor.first !== null && !items.some(item => key(item) === anchor.first) && scroller?.scrollTop === anchor.position) list?.restoreAnchor(anchor.row)
          pendingAnchor = null
          await tick()
          if (destroyed) return false
          const currentEdge = edgeKey(older ? items[0] : items.at(-1))
          const near = scroller !== null && atEdge(scroller, older)
          if (!(older ? earlier : newer) || !near || currentEdge !== edge) break
        } while (true)
        return true
      } catch (e) {
        error = errorText(e)
        return false
      } finally {
        busy = false
        pendingAnchor = null
        request = null
      }
    })
    return request
  }

  async function present(): Promise<boolean> {
    if (request) { await request; return present() }
    if (!newer) {
      list?.scrollToEnd()
      atBottom = true
      return true
    }
    busy = true
    loading = 'present'
    error = ''
    request = Promise.resolve().then(async () => {
      try {
        if (await onpresent?.() === false) { error = 'Could not load messages.'; return false }
        await tick()
        if (destroyed) return false
        list?.scrollToEnd()
        atBottom = true
        return true
      } catch (e) { error = errorText(e); return false }
      finally { busy = false; request = null }
    })
    return request
  }

  function updatePosition(el: HTMLElement | null | undefined) {
    if (!el) return
    const gap = el.scrollHeight - el.scrollTop - el.clientHeight
    atBottom = !newer && gap < 48
  }

  $effect(() => {
    const el = scroller
    if (!el) return
    let last = el.scrollTop
    let touchY: number | null = null
    const prefetch = (delta: number) => {
      if (delta < 0 && el.scrollTop <= el.clientHeight) loadEarlier()
      else if (delta > 0 && el.scrollHeight - el.scrollTop - el.clientHeight <= el.clientHeight) loadPage('newer')
    }
    const wheel = (event: WheelEvent) => prefetch(event.deltaY)
    const touchstart = (event: TouchEvent) => { touchY = event.touches.length === 1 ? event.touches[0]?.clientY ?? null : null }
    const touchmove = (event: TouchEvent) => {
      if (event.touches.length !== 1) { touchY = null; return }
      const touch = event.touches[0]
      if (touch === undefined) { touchY = null; return }
      const y = touch.clientY
      if (touchY !== null) prefetch(touchY - y)
      touchY = y
    }
    const scroll = () => {
      const top = el.scrollTop
      if (pendingAnchor && items.length && edgeKey(items[0]) === pendingAnchor.first) {
        pendingAnchor.row = list?.captureAnchor()
        pendingAnchor.position = top
      }
      updatePosition(el)
      prefetch(top - last)
      last = top
    }
    el.addEventListener('scroll', scroll, { passive: true })
    el.addEventListener('wheel', wheel, { passive: true })
    el.addEventListener('touchstart', touchstart, { passive: true })
    el.addEventListener('touchmove', touchmove, { passive: true })
    return () => {
      el.removeEventListener('scroll', scroll)
      el.removeEventListener('wheel', wheel)
      el.removeEventListener('touchstart', touchstart)
      el.removeEventListener('touchmove', touchmove)
    }
  })

  $effect(() => {
    items.length
    updatePosition(scroller)
  })

  // Channel filters can hide a whole page. Fill the viewport before waiting
  // for a scroll the reader cannot make. A failed request waits for a retry.
  // A list that shares another list's pager (a thread panel beside its DM) sets
  // fill to false: filling it would page the whole DM history under the reader.
  $effect(() => {
    items.length
    if (fill && scroller && scroller.clientHeight > 0 && scroller.scrollHeight <= scroller.clientHeight && !busy && !error) {
      if (earlier) loadEarlier()
    }
  })

  export function scrollToIndex(i: number, align = 'center') { list?.scrollToIndex(i, align) }
  export function scrollToEnd() { list?.scrollToEnd() }
</script>

<!-- Its fixed height keeps loading and beginning markers outside the anchor calculation. -->
<div class="history" role="status" aria-live="polite">
  {#if busy}<span class="skeleton" role="progressbar" aria-label={loading === 'older' ? 'Loading earlier messages' : 'Loading newer messages'}></span>
  {:else if error}<button onclick={() => loading === 'present' ? present() : loadPage(loading)}>Could not load messages. Retry</button>
  {:else if !earlier && beginning}<span>{beginning}</span>{/if}
</div>
<VirtualList bind:this={list} {scroller} {items} {key} {estimate} chat follow={!newer && !busy}>
  {#snippet children(item: T, index: number)}{@render renderRow(item, index)}{/snippet}
</VirtualList>
{#if onpresent && !atBottom}
  <div class="jump-bar" use:below={scroller}><button class="btn jump-present" aria-label="Jump to present" disabled={busy} onclick={present}>Jump to present <span aria-hidden="true">↓</span></button></div>
{/if}

<style>
  /* The pill gets its own row under the list instead of floating over the
     last post, so it never covers a row or one of its buttons. */
  .jump-bar { flex: none; display: flex; justify-content: center; padding: var(--s1) 0 var(--s2); background: var(--main); }
  .jump-present { min-height: 44px; border-radius: 99px; box-shadow: 0 2px 10px #0003; white-space: nowrap; }
  .history { height: 36px; display: flex; align-items: center; justify-content: center; color: var(--faint); font-size: var(--t-sm); }
  .history button { color: var(--muted); font-size: inherit; }
  .skeleton { width: 120px; height: 8px; border-radius: 4px; background: var(--line); }
</style>
