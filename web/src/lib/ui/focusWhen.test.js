import test from 'node:test'
import assert from 'node:assert/strict'
import { focusWhen, revealWhen } from './focusWhen.ts'

function frames(t) {
  const queued = []
  const saved = globalThis.requestAnimationFrame
  globalThis.requestAnimationFrame = (f) => queued.push(f)
  t.after(() => { globalThis.requestAnimationFrame = saved })
  return () => queued.splice(0).forEach((f) => f())
}

test('focusWhen focuses a drawn node on the next frame while it is on', (t) => {
  const flush = frames(t)
  const calls = []
  const node = { isConnected: true, focus: (o) => calls.push(o) }
  focusWhen(node, true)
  const off = focusWhen(node, false)
  off.update(false)
  off.update(true)
  // A node the list removed before the frame is not focused.
  focusWhen({ isConnected: false, focus: (o) => calls.push(o) }, true)
  assert.equal(calls.length, 0)
  flush()
  assert.deepEqual(calls, [{ preventScroll: true }, { preventScroll: true }])
})

test('revealWhen lets the focus scroll the node into view', (t) => {
  const flush = frames(t)
  const calls = []
  revealWhen({ isConnected: true, focus: (o) => calls.push(o) }, true)
  flush()
  assert.deepEqual(calls, [{ preventScroll: false }])
})
