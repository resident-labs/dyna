interface Options {
  enabled: boolean
  back: () => void
}

// Only a clear, single-touch right swipe on the header navigates.
export function threadHeaderSwipe(node: HTMLElement, options: Options) {
  const doc = node.ownerDocument
  const touches = new Set<number>()
  let start: { id: number; x: number; y: number } | null = null
  function down(event: PointerEvent) {
    if (!options.enabled || event.pointerType === 'mouse') return
    touches.add(event.pointerId)
    if (touches.size !== 1 || !event.isPrimary) { start = null; return }
    const target = event.target
    const view = doc.defaultView
    if (!view || !(target instanceof view.Element) || !node.contains(target) ||
      target.closest('button, a, input, textarea, select, [contenteditable], [role="button"]')) return
    start = { id: event.pointerId, x: event.clientX, y: event.clientY }
  }
  function move(event: PointerEvent) {
    if (!start || event.pointerId !== start.id) return
    const x = Math.abs(event.clientX - start.x), y = Math.abs(event.clientY - start.y)
    if (y > 30 && y > x) start = null
  }
  function up(event: PointerEvent) {
    touches.delete(event.pointerId)
    const from = start
    if (!from || event.pointerId !== from.id) return
    start = null
    const x = event.clientX - from.x, y = Math.abs(event.clientY - from.y)
    if (options.enabled && x >= 60 && y <= 30 && x >= y * 2 && doc.getSelection()?.isCollapsed !== false) options.back()
  }
  function cancel(event: PointerEvent) {
    touches.delete(event.pointerId)
    start = null
  }
  doc.addEventListener('pointerdown', down)
  doc.addEventListener('pointermove', move)
  doc.addEventListener('pointerup', up)
  doc.addEventListener('pointercancel', cancel)
  return {
    update(next: Options) { options = next; start = null; touches.clear() },
    destroy() {
      doc.removeEventListener('pointerdown', down)
      doc.removeEventListener('pointermove', move)
      doc.removeEventListener('pointerup', up)
      doc.removeEventListener('pointercancel', cancel)
    },
  }
}
