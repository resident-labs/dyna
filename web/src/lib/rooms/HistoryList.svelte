<script>
  import { onDestroy, tick } from 'svelte'
  import VirtualList from '../ui/VirtualList.svelte'

  let { scroller, items, key, estimate, earlier = false, newer = false, onearlier, onnewer, onpresent, beginning, onhistory, fill = true, atBottom = $bindable(true), children: renderRow } = $props()
  let list = $state()
  let busy = $state(false)
  let error = $state('')
  let far = $state(false)
  let loading = $state('older')
  let request = null
  let pendingAnchor = null
  // A request outlives the view it started in: a reader who opens another page
  // mid-load would otherwise have the paging loop read a list it no longer owns.
  let destroyed = false
  onDestroy(() => (destroyed = true))

  // Both directions, filtered pages and jumps share one request.
  export function loadEarlier() { return loadPage('older') }

  function loadPage(direction) {
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
          const anchor = { first: items.length ? key(items[0]) : null, position: scroller?.scrollTop, row: list?.captureAnchor() }
          pendingAnchor = anchor
          const edge = items.length ? key(older ? items[0] : items.at(-1)) : null
          const nearEdge = () => !!scroller && (older ? scroller.scrollTop : scroller.scrollHeight - scroller.scrollTop - scroller.clientHeight) <= scroller.clientHeight
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
          const currentEdge = items.length ? key(older ? items[0] : items.at(-1)) : null
          const near = scroller && (older ? scroller.scrollTop : scroller.scrollHeight - scroller.scrollTop - scroller.clientHeight) <= scroller.clientHeight
          if (!(older ? earlier : newer) || !near || currentEdge !== edge) break
        } while (true)
        return true
      } catch (e) {
        error = String(e?.message || e)
        return false
      } finally {
        busy = false
        pendingAnchor = null
        request = null
      }
    })
    return request
  }

  async function present() {
    if (request) { await request; return present() }
    busy = true
    loading = 'present'
    error = ''
    request = Promise.resolve().then(async () => {
      try {
        if (await onpresent() === false) { error = 'Could not load messages.'; return false }
        await tick()
        if (destroyed) return false
        list?.scrollToEnd()
        far = false
        atBottom = true
        return true
      } catch (e) { error = String(e?.message || e); return false }
      finally { busy = false; request = null }
    })
    return request
  }

  function updatePosition(el) {
    if (!el) return
    const gap = el.scrollHeight - el.scrollTop - el.clientHeight
    far = gap > el.clientHeight
    atBottom = !newer && gap < 48
  }

  $effect(() => {
    const el = scroller
    if (!el) return
    let last = el.scrollTop
    let touchY = null
    const prefetch = delta => {
      if (delta < 0 && el.scrollTop <= el.clientHeight) loadEarlier()
      else if (delta > 0 && el.scrollHeight - el.scrollTop - el.clientHeight <= el.clientHeight) loadPage('newer')
    }
    const wheel = event => prefetch(event.deltaY)
    const touchstart = event => { touchY = event.touches.length === 1 ? event.touches[0].clientY : null }
    const touchmove = event => {
      if (event.touches.length !== 1) { touchY = null; return }
      const y = event.touches[0].clientY
      if (touchY !== null) prefetch(touchY - y)
      touchY = y
    }
    const scroll = () => {
      const top = el.scrollTop
      if (pendingAnchor && items.length && key(items[0]) === pendingAnchor.first) {
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

  export function scrollToIndex(i, align = 'center') { list?.scrollToIndex(i, align) }
  export function scrollToEnd() { list?.scrollToEnd() }
</script>

<!-- Its fixed height keeps loading and beginning markers outside the anchor calculation. -->
<div class="history" role="status" aria-live="polite">
  {#if busy}<span class="skeleton" role="progressbar" aria-label={loading === 'older' ? 'Loading earlier messages' : 'Loading newer messages'}></span>
  {:else if error}<button onclick={() => loading === 'present' ? present() : loadPage(loading)}>Could not load messages. Retry</button>
  {:else if !earlier && beginning}<span>{beginning}</span>{/if}
</div>
<VirtualList bind:this={list} {scroller} {items} {key} {estimate} chat follow={!newer && !busy}>
  {#snippet children(item, index)}{@render renderRow(item, index)}{/snippet}
</VirtualList>
{#if onpresent && (newer || far)}
  <div class="jump-slot"><button class="btn jump-present" aria-label="Jump to present" disabled={busy} onclick={present}>Jump to present <span aria-hidden="true">↓</span></button></div>
{/if}

<style>
  .jump-slot { height: 0; position: sticky; bottom: 12px; display: flex; align-items: flex-start; justify-content: center; z-index: 2; }
  .jump-present { transform: translateY(-100%); border-radius: 99px; box-shadow: 0 2px 10px #0003; white-space: nowrap; }
  .history { height: 36px; display: flex; align-items: center; justify-content: center; color: var(--faint); font-size: var(--t-sm); }
  .history button { color: var(--muted); font-size: inherit; }
  .skeleton { width: 120px; height: 8px; border-radius: 4px; background: var(--line); }
</style>
