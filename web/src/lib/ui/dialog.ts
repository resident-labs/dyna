import { tick } from 'svelte'
import type { Action } from 'svelte/action'

interface DialogOptions {
  close: () => unknown
  active?: boolean
  busy?: boolean
  initial?: () => HTMLElement | null | undefined
  preventScroll?: boolean
  select?: boolean
}

const dialogs: { node: HTMLElement }[] = []
const controls = 'button, a[href], input, textarea, select, summary, [tabindex], [contenteditable="true"]'

// A nested card can mount before its parent. DOM containment, then mount
// order for siblings, determines which dialog owns the keyboard.
const activeDialog = () => dialogs.filter((entry) => !dialogs.some((other) => other !== entry && entry.node.contains(other.node))).at(-1)

// Apply to the dialog card. Its parent is the backdrop. Only the top
// dialog handles keys, so Escape cannot also close the page behind it.
export const dialog = ((node, options: DialogOptions) => {
  const backdrop = node.parentElement
  if (!backdrop) throw new TypeError('A dialog needs a parent backdrop.')
  const entry = { node }
  let alive = true
  let active = false
  let previous: Element | null = null
  const top = () => active && activeDialog() === entry
  const otherModal = () => {
    const modal = document.activeElement?.closest?.('[aria-modal="true"]')
    return modal && modal !== node && !modal.contains(node)
  }
  const dismiss = () => { if (!options.busy) options.close() }
  const click = (event: MouseEvent) => {
    if (event.target === backdrop && top()) dismiss()
  }
  const keydown = (event: KeyboardEvent) => {
    if (!top() || otherModal()) return
    if (event.key === 'Escape') {
      // Busy: a control inside the dialog owns Escape. So does an armed inline
      // confirm, but only for keys pressed inside it; elsewhere Escape closes.
      if (options.busy && node.contains(event.target as Node | null)) return
      if (node.querySelector('[data-esc]')?.contains(event.target as Node | null)) return
      event.preventDefault()
      event.stopImmediatePropagation()
      dismiss()
    } else if (event.key === 'Tab') {
      const available = [...node.querySelectorAll<HTMLElement>(controls)].filter((el) => (!('disabled' in el) || !el.disabled) && el.tabIndex >= 0 && el.getClientRects().length)
      const first = available[0], last = available.at(-1)
      const focused = document.activeElement
      if (!first || !node.contains(focused) || focused === node || (event.shiftKey ? focused === first : focused === last)) {
        event.preventDefault()
        event.stopImmediatePropagation()
        ;(event.shiftKey ? last : first)?.focus()
        if (!first) node.focus()
      }
    }
  }
  const deactivate = () => {
    if (!active) return
    const restore = top() && !otherModal()
    const index = dialogs.indexOf(entry)
    if (index >= 0) dialogs.splice(index, 1)
    active = false
    if (restore) {
      const lower = activeDialog()?.node
      const target = lower && !lower.contains(previous) ? lower : previous
      if (target?.isConnected && 'focus' in target && typeof target.focus === 'function') target.focus()
    }
  }
  const activate = () => {
    if (!alive || active) return
    previous = document.activeElement
    active = true
    dialogs.push(entry)
    tick().then(() => {
      if (!alive || !top()) return
      const initial = options.initial?.() || node
      initial.focus({ preventScroll: !!options.preventScroll })
      if (options.select && 'select' in initial && typeof initial.select === 'function') initial.select()
    })
  }
  window.addEventListener('keydown', keydown, true)
  backdrop.addEventListener('click', click)
  if (options.active ?? true) activate()
  return {
    update(value: DialogOptions) {
      const next = value.active ?? true
      options = value
      if (next === active) return
      if (next) activate()
      else deactivate()
    },
    destroy() {
      alive = false
      deactivate()
      window.removeEventListener('keydown', keydown, true)
      backdrop.removeEventListener('click', click)
    },
  }
}) satisfies Action<HTMLElement, DialogOptions>
