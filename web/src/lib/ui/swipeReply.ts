import type { Action } from 'svelte/action'

// A left swipe replies on release. Listen for touch events so vertical
// scrolling and pinch zoom keep the browser's normal behavior. Only a
// clear horizontal gesture cancels the browser's default touch action.
export const swipeReply = ((node, reply: () => unknown) => {
  const threshold = 64
  let start: { x: number; y: number; id: number; at: number } | null = null
  let dragging = false
  let suppressClickUntil = 0

  function reset() {
    start = null
    dragging = false
    node.classList.remove('swipe-drag', 'swipe-ready')
    node.style.removeProperty('--reply-transform')
  }

  function begin(e: TouchEvent) {
    reset()
    if (!window.matchMedia('(pointer: coarse)').matches || e.touches.length !== 1 || (e.target as Element).closest('input, textarea, select, iframe, pre, table, [contenteditable="true"]')) return
    if (window.getSelection()?.toString()) return
    const touch = e.touches[0]
    if (!touch) { reset(); return }
    start = { x: touch.clientX, y: touch.clientY, id: touch.identifier, at: performance.now() }
    node.style.setProperty('--reply-cue-y', `${touch.clientY - node.getBoundingClientRect().top}px`)
  }

  function move(e: TouchEvent) {
    if (!start) return
    if (e.touches.length !== 1) { reset(); return }
    const touch = e.touches[0]
    if (!touch) { reset(); return }
    if (touch.identifier !== start.id) { reset(); return }
    const dx = touch.clientX - start.x
    const dy = touch.clientY - start.y
    if (!dragging) {
      if (performance.now() - start.at > 500 || window.getSelection()?.toString()) { reset(); return }
      if (Math.abs(dy) > 12 && Math.abs(dy) >= Math.abs(dx)) { reset(); return }
      if (dx > 12) { reset(); return }
      if (dx > -12 || Math.abs(dx) < Math.abs(dy) * 1.4) return
      dragging = true
      node.classList.add('swipe-drag')
    }
    // A non-cancelable event means the browser already claimed the scroll.
    if (!e.cancelable) { reset(); return }
    e.preventDefault()
    const distance = Math.max(0, -dx)
    node.style.setProperty('--reply-transform', `translateX(${-Math.min(96, distance)}px)`)
    node.classList.toggle('swipe-ready', distance >= threshold)
  }

  function end(e: TouchEvent) {
    if (!start) return
    const id = start.id
    const touch = [...e.changedTouches].find((t) => t.identifier === id)
    if (!touch) { reset(); return }
    // Releasing after reversing the swipe should not send a reply action.
    const ready = dragging && e.touches.length === 0 && start.x - touch.clientX >= threshold
    if (dragging) {
      if (e.cancelable) e.preventDefault()
      suppressClickUntil = Date.now() + 500
    }
    reset()
    if (ready) reply()
  }

  function click(e: MouseEvent) {
    if (Date.now() >= suppressClickUntil) return
    e.preventDefault()
    e.stopImmediatePropagation()
  }

  node.classList.add('swipeable')
  node.addEventListener('touchstart', begin, { passive: true })
  node.addEventListener('touchmove', move, { passive: false })
  node.addEventListener('touchend', end, { passive: false })
  node.addEventListener('touchcancel', reset)
  node.addEventListener('contextmenu', reset)
  node.addEventListener('click', click, true)
  return {
    update(next: () => unknown) { reply = next },
    destroy() {
      reset()
      node.classList.remove('swipeable')
      node.style.removeProperty('--reply-cue-y')
      node.removeEventListener('touchstart', begin)
      node.removeEventListener('touchmove', move)
      node.removeEventListener('touchend', end)
      node.removeEventListener('touchcancel', reset)
      node.removeEventListener('contextmenu', reset)
      node.removeEventListener('click', click, true)
    },
  }
}) satisfies Action<HTMLElement, () => unknown>
