import { tick } from 'svelte'

const dialogs = []
const controls = 'button, a[href], input, textarea, select, summary, [tabindex], [contenteditable="true"]'

// A nested card can mount before its parent. DOM containment, then mount
// order for siblings, determines which dialog owns the keyboard.
const activeDialog = () => dialogs.filter((entry) => !dialogs.some((other) => other !== entry && entry.node.contains(other.node))).at(-1)

// Apply to the dialog card. Its parent is the backdrop. Only the top
// dialog handles keys, so Escape cannot also close the page behind it.
export function dialog(node, options) {
  const previous = document.activeElement
  const backdrop = node.parentElement
  const entry = { node }
  dialogs.push(entry)
  let alive = true
  const top = () => activeDialog() === entry
  const otherModal = () => {
    const modal = document.activeElement?.closest?.('[aria-modal="true"]')
    return modal && modal !== node && !modal.contains(node)
  }
  const dismiss = () => { if (!options.busy) options.close() }
  const click = (event) => {
    if (event.target === backdrop && top()) dismiss()
  }
  const keydown = (event) => {
    if (!top() || otherModal()) return
    if (event.key === 'Escape') {
      // Busy: a control inside the dialog owns Escape. So does an armed inline
      // confirm, but only for keys pressed inside it; elsewhere Escape closes.
      if (options.busy && node.contains(event.target)) return
      if (node.querySelector('[data-esc]')?.contains(event.target)) return
      event.preventDefault()
      event.stopImmediatePropagation()
      dismiss()
    } else if (event.key === 'Tab') {
      const available = [...node.querySelectorAll(controls)].filter((el) => !el.disabled && el.tabIndex >= 0 && el.getClientRects().length)
      const first = available[0], last = available.at(-1)
      const active = document.activeElement
      if (!first || !node.contains(active) || active === node || (event.shiftKey ? active === first : active === last)) {
        event.preventDefault()
        event.stopImmediatePropagation()
        ;(event.shiftKey ? last : first)?.focus()
        if (!first) node.focus()
      }
    }
  }
  tick().then(() => {
    if (!alive || !top()) return
    const initial = options.initial?.() || node
    initial.focus({ preventScroll: !!options.preventScroll })
    if (options.select) initial.select?.()
  })
  window.addEventListener('keydown', keydown, true)
  backdrop.addEventListener('click', click)
  return {
    update(value) { options = value },
    destroy() {
      alive = false
      const restore = top() && !otherModal()
      dialogs.splice(dialogs.indexOf(entry), 1)
      window.removeEventListener('keydown', keydown, true)
      backdrop.removeEventListener('click', click)
      if (restore) {
        const lower = activeDialog()?.node
        const target = lower && !lower.contains(previous) ? lower : previous
        if (target?.isConnected) target.focus()
      }
    },
  }
}
