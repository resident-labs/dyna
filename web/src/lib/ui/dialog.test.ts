import { test } from 'node:test'
import assert from 'node:assert/strict'
import { tick } from 'svelte'
import { dialog } from './dialog.ts'

// The harness stands in for a dialog card, its backdrop and its controls.
// dialog reads only these members of an element.
interface FakeBackdrop {
  addEventListener(type: string, fn: (event: { target: unknown }) => void): void
  removeEventListener(type: string, fn: (event: { target: unknown }) => void): void
}

interface FakeElement {
  isConnected: boolean
  tabIndex: number
  disabled?: boolean
  parentElement?: FakeBackdrop | null
  focus(options?: FocusOptions): void
  getClientRects(): ArrayLike<unknown>
  querySelector?: (selector: string) => unknown
  querySelectorAll?: (selectors: string) => Iterable<unknown>
  contains?: (other: unknown) => boolean
  closest?(selector: string): unknown
}

interface FakeKeyEvent {
  key: string
  shiftKey?: boolean
  target?: unknown
  prevented?: boolean
  stopped: boolean
  preventDefault(): void
  stopImmediatePropagation(): void
}

interface FakeDocument {
  activeElement: FakeElement | null
}

// A fake card stands in for the element the action is typed against.
const asCard = (node: FakeElement): HTMLElement => node as HTMLElement

function setup() {
  const page: FakeDocument = { activeElement: null }
  const listeners = new Set<(event: FakeKeyEvent) => void>()
  Object.assign(globalThis, {
    document: page,
    window: {
      addEventListener: (_type: string, fn: (event: FakeKeyEvent) => void) => { listeners.add(fn) },
      removeEventListener: (_type: string, fn: (event: FakeKeyEvent) => void) => { listeners.delete(fn) },
    },
  })
  const element = (more: Partial<FakeElement> = {}): FakeElement => {
    const base: FakeElement = { isConnected: true, tabIndex: 0, disabled: false, focus() { page.activeElement = this }, getClientRects: () => [1] }
    return Object.assign(base, more)
  }
  const outside = element()
  outside.focus()
  function card(children: FakeElement[] = []) {
    let click: ((event: { target: unknown }) => void) | null | undefined
    const backdrop: FakeBackdrop = { addEventListener: (_type, fn) => { click = fn }, removeEventListener: () => { click = null } }
    const node = element({ parentElement: backdrop, querySelectorAll: () => children, querySelector: () => null, contains: (el) => el === node || children.some((child) => child === el) })
    return { node, backdrop, click: (target: unknown = backdrop) => click?.({ target }) }
  }
  function key(key: string, shiftKey = false): FakeKeyEvent {
    const event: FakeKeyEvent = { key, shiftKey, prevented: false, stopped: false, preventDefault() { this.prevented = true }, stopImmediatePropagation() { this.stopped = true } }
    for (const fn of listeners) { fn(event); if (event.stopped) break }
    return event
  }
  return { element, card, key, outside, listeners }
}

test('dialog starts at the chosen control, wraps Tab, and restores focus', async () => {
  const s = setup(), first = s.element(), last = s.element()
  const card = s.card([first, s.element({ disabled: true }), s.element({ getClientRects: () => [] }), last])
  const action = dialog(asCard(card.node), { close() {}, initial: () => asCard(first) })
  await tick()
  assert.equal(document.activeElement, first)
  assert.equal(s.key('Tab', true).prevented, true)
  assert.equal(document.activeElement, last)
  s.key('Tab')
  assert.equal(document.activeElement, first)
  s.outside.focus()
  s.key('Tab')
  assert.equal(document.activeElement, first)
  action.destroy()
  assert.equal(document.activeElement, s.outside)
  assert.equal(s.listeners.size, 0)
})

test('an inactive dialog leaves the page alone until it opens and restores focus when it closes', async () => {
  const s = setup(), first = s.element(), card = s.card([first])
  const action = dialog(asCard(card.node), { close() {}, active: false })
  await tick()
  assert.equal(document.activeElement, s.outside)
  assert.equal(s.key('Tab').prevented, false)
  action.update({ close() {}, active: true })
  await tick()
  assert.equal(document.activeElement, card.node)
  s.outside.focus()
  s.key('Tab')
  assert.equal(document.activeElement, first)
  action.update({ close() {}, active: false })
  assert.equal(document.activeElement, s.outside)
  assert.equal(s.key('Escape').stopped, false)
  action.destroy()
  assert.equal(s.listeners.size, 0)
})

test('dialog handles busy changes and only backdrop clicks dismiss', () => {
  const s = setup(), card = s.card()
  let closed = 0
  const close = () => closed++
  const action = dialog(asCard(card.node), { close, busy: true })
  assert.equal(s.key('Escape').stopped, true)
  card.click()
  assert.equal(closed, 0)
  action.update({ close, busy: false })
  card.click(card.node)
  assert.equal(closed, 0)
  card.click()
  s.key('Escape')
  assert.equal(closed, 2)
  action.destroy()
})

test('a busy dialog leaves Escape from its own controls to them', () => {
  const s = setup(), field = s.element(), card = s.card([field])
  let closed = 0
  const action = dialog(asCard(card.node), { close: () => closed++, busy: true })
  const event: FakeKeyEvent = { key: 'Escape', target: field, stopped: false, preventDefault() {}, stopImmediatePropagation() { this.stopped = true } }
  for (const fn of s.listeners) fn(event)
  assert.equal(event.stopped, false)
  assert.equal(closed, 0)
  action.destroy()
})

test('an armed inline confirm owns Escape, so the dialog stays open', () => {
  const s = setup(), cancel = s.element(), card = s.card([cancel])
  const other = s.element()
  card.node.querySelector = (selector) => (selector === '[data-esc]' ? { contains: (el: unknown) => el === cancel } : null)
  let closed = 0
  const action = dialog(asCard(card.node), { close: () => closed++ })
  const press = (target: FakeElement): FakeKeyEvent => {
    const event: FakeKeyEvent = { key: 'Escape', target, stopped: false, preventDefault() {}, stopImmediatePropagation() { this.stopped = true } }
    for (const fn of s.listeners) fn(event)
    return event
  }
  assert.equal(press(cancel).stopped, false)
  assert.equal(closed, 0)
  // Focus on another control: nothing owns Escape, so the dialog closes.
  card.node.contains = (el) => el === card.node || el === cancel || el === other
  assert.equal(press(other).stopped, true)
  assert.equal(closed, 1)
  action.destroy()
})

test('nested dialogs own Escape and return focus to the lower dialog', async () => {
  const s = setup(), first = s.card(), second = s.card()
  let lower = 0, upper = 0
  const a = dialog(asCard(first.node), { close: () => lower++ })
  await tick()
  const b = dialog(asCard(second.node), { close: () => upper++ })
  await tick()
  s.key('Escape')
  assert.equal(lower, 0)
  assert.equal(upper, 1)
  b.destroy()
  assert.equal(document.activeElement, first.node)
  s.key('Escape')
  assert.equal(lower, 1)
  a.destroy()
})

test('an empty dialog traps Tab and a destroyed dialog never takes initial focus', async () => {
  const s = setup(), card = s.card()
  const action = dialog(asCard(card.node), { close() {} })
  s.key('Tab')
  assert.equal(document.activeElement, card.node)
  action.destroy()
  await tick()
  assert.equal(document.activeElement, s.outside)
})


test('a newer dialog that does not use the action keeps its keyboard and focus', async () => {
  const s = setup(), card = s.card()
  let closed = 0
  const action = dialog(asCard(card.node), { close: () => closed++ })
  await tick()
  const modal = { contains: () => false }
  const other = s.element({ closest: () => modal })
  other.focus()
  assert.equal(s.key('Escape').stopped, false)
  assert.equal(s.key('Tab').prevented, false)
  assert.equal(closed, 0)
  action.destroy()
  assert.equal(document.activeElement, other)
})

test('a menu opened above a dialog keeps Escape and Tab, so the dialog stays open', async () => {
  const s = setup(), card = s.card()
  let closed = 0
  const action = dialog(asCard(card.node), { close: () => closed++ })
  await tick()
  // App renders the room and session menus outside the drawer and the sheet.
  const menu = s.element({ contains: () => false })
  const item = s.element({ closest: (selector) => (selector.includes('[role="menu"]') ? menu : null) })
  item.focus()
  assert.equal(s.key('Escape').stopped, false)
  assert.equal(s.key('Tab').prevented, false)
  assert.equal(closed, 0)
  action.destroy()
})

test('a sheet lets an inner control take Escape first, then closes on the next one', async () => {
  const s = setup(), card = s.card()
  let closed = 0, cancelled = 0
  const action = dialog(asCard(card.node), { close: () => closed++, escape: () => { cancelled++; return true } })
  await tick()
  const first = s.key('Escape')
  assert.equal(cancelled, 1)
  assert.equal(closed, 0)
  // The key is handled, so no lower handler cancels the same work again.
  assert.equal(first.prevented, true)
  action.update({ close: () => closed++, escape: () => false })
  s.key('Escape')
  assert.equal(closed, 1)
  action.destroy()
})

test('a nested dialog owns keys even when its parent action mounts later', async () => {
  const s = setup(), inner = s.card(), outer = s.card([inner.node])
  let innerClosed = 0, outerClosed = 0
  const child = dialog(asCard(inner.node), { close: () => innerClosed++ })
  const parent = dialog(asCard(outer.node), { close: () => outerClosed++ })
  await tick()
  assert.equal(document.activeElement, inner.node)
  s.key('Escape')
  assert.equal(innerClosed, 1)
  assert.equal(outerClosed, 0)
  child.destroy()
  assert.equal(document.activeElement, outer.node)
  parent.destroy()
  assert.equal(document.activeElement, s.outside)
})

test('focus options preserve a guided editor scroll position', async () => {
  const s = setup(), card = s.card()
  let options: FocusOptions | undefined
  const first = s.element({ focus(value) { options = value } })
  const action = dialog(asCard(card.node), { close() {}, initial: () => asCard(first), preventScroll: true })
  await tick()
  assert.deepEqual(options, { preventScroll: true })
  action.destroy()
})

test('InlineConfirm marks itself, focuses Cancel and cancels on Escape', async () => {
  const { readFileSync } = await import('node:fs')
  const text = readFileSync(new URL('./InlineConfirm.svelte', import.meta.url), 'utf8')
  assert.match(text, /data-esc/)
  assert.match(text, /onkeydown=\{[^}]*Escape[^}]*stopPropagation[^}]*oncancel/s)
  assert.match(text, /\.focus\(/)
})
