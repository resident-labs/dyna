import { test } from 'node:test'
import assert from 'node:assert/strict'
import { swipeReply } from './swipeReply.ts'

function fixture({ coarse = true, interactive = false, selected = false } = {}) {
  globalThis.window = { matchMedia: () => ({ matches: coarse }), getSelection: () => selected ? 'selected text' : '' }
  class Post extends EventTarget {
    classes = new Set()
    properties = new Map()
    classList = { add: (...keys) => keys.forEach((k) => this.classes.add(k)), remove: (...keys) => keys.forEach((k) => this.classes.delete(k)), toggle: (k, on) => on ? this.classes.add(k) : this.classes.delete(k) }
    style = { setProperty: (k, v) => this.properties.set(k, v), removeProperty: (k) => this.properties.delete(k) }
    closest() { return interactive }
    getBoundingClientRect() { return { top: 50 } }
  }
  const node = new Post()
  let replies = 0
  const action = swipeReply(node, () => replies++)
  const touch = (x, y = 100, identifier = 1) => ({ clientX: x, clientY: y, identifier })
  function event(type, touches, changedTouches = touches, cancelable = true) {
    const e = new Event(type, { cancelable })
    Object.assign(e, { touches, changedTouches })
    node.dispatchEvent(e)
    return e
  }
  const start = (x = 260, y = 100) => event('touchstart', [touch(x, y)])
  const move = (x, y = 100) => event('touchmove', [touch(x, y)])
  const end = (x, y = 100) => event('touchend', [], [touch(x, y)])
  return { node, action, touch, event, start, move, end, replies: () => replies }
}

test('a deliberate left swipe shows the cue and replies once on release', () => {
  const f = fixture()
  f.start()
  assert.equal(f.move(180).defaultPrevented, true)
  assert.equal(f.node.classes.has('swipe-ready'), true)
  assert.equal(f.replies(), 0)
  f.end(180)
  assert.equal(f.replies(), 1)
  assert.equal(f.node.properties.has('--reply-transform'), false)
  const click = new Event('click', { cancelable: true })
  f.node.dispatchEvent(click)
  assert.equal(click.defaultPrevented, true)
  f.end(180)
  assert.equal(f.replies(), 1)
  f.action.destroy()
})

test('right swipes, vertical scrolls, short drags, and reversed drags do not reply', () => {
  for (const [x, y] of [[330, 100], [250, 180], [220, 100]]) {
    const f = fixture(); f.start(); const move = f.move(x, y); f.end(x, y)
    assert.equal(f.replies(), 0)
    if (x > 260 || y > 100) assert.equal(move.defaultPrevented, false)
  }
  const f = fixture(); f.start(); f.move(150); f.move(250); f.end(250)
  assert.equal(f.replies(), 0)
})

test('fine pointers, selectable controls, and selected text keep native behavior', () => {
  for (const options of [{ coarse: false }, { interactive: true }, { selected: true }]) {
    const f = fixture(options); f.start()
    assert.equal(f.move(150).defaultPrevented, false)
    f.end(150)
    assert.equal(f.replies(), 0)
  }
})

test('cancellation, context menus, multiple fingers, and browser scroll ownership cancel the gesture', () => {
  for (const cancel of ['touchcancel', 'contextmenu', 'multitouch', 'scroll']) {
    const f = fixture(); f.start(); f.move(180)
    if (cancel === 'multitouch') f.event('touchmove', [f.touch(160), f.touch(200, 100, 2)])
    else if (cancel === 'scroll') f.event('touchmove', [f.touch(160)], [], false)
    else f.node.dispatchEvent(new Event(cancel))
    f.end(150)
    assert.equal(f.replies(), 0, cancel)
    assert.equal(f.node.classes.has('swipe-drag'), false)
  }
})

test('a long press keeps text selection and menus available', (t) => {
  let clock = 0
  t.mock.method(performance, 'now', () => clock)
  const f = fixture(); f.start()
  clock = 600
  assert.equal(f.move(150).defaultPrevented, false)
  f.end(150)
  assert.equal(f.replies(), 0)
})
