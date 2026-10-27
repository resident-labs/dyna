import { test } from 'node:test'
import assert from 'node:assert/strict'
import { clampHeight, closedDock, dockOf, dockReduce, legacyView } from './termDock.ts'

test('the dock height stays between 120 px and 80% of the main area', () => {
  assert.equal(clampHeight(40, 800), 120)
  assert.equal(clampHeight(500, 800), 500)
  assert.equal(clampHeight(900, 800), 640)
  assert.equal(clampHeight(500, 100), 120) // a tiny main area still gets the minimum
  assert.equal(clampHeight(NaN, 800), 280)
})

test('toggle opens and hides; hiding drops maximize but keeps height and tabs', () => {
  let v = dockReduce(closedDock(), { type: 'resize', height: 400, avail: 1000 })
  v = dockReduce(dockReduce(v, { type: 'tab', ref: 'r', id: 't1' }), { type: 'maximize' })
  assert.deepEqual([v.open, v.maxed], [true, true])
  v = dockReduce(v, { type: 'toggle' })
  assert.deepEqual([v.open, v.maxed, v.height, v.active], [false, false, 400, { r: 't1' }])
  assert.equal(dockReduce(v, { type: 'toggle' }).open, true)
})

test('a saved dock is read back and broken values fall back to closed', () => {
  assert.deepEqual(dockOf({ open: true, height: 9999, maxed: true, active: { r: 't1', x: 3 } }), { open: true, height: 9999, maxed: true, active: { r: 't1' } })
  assert.deepEqual(dockOf('nope'), closedDock())
  assert.equal(dockOf({ open: false, maxed: true }).maxed, false)
})

test('the old terminal view opens the room feed with the dock open', () => {
  assert.deepEqual(legacyView('terminal', 'general'), { view: 'general', dock: true })
  assert.deepEqual(legacyView('library', 'general'), { view: 'library', dock: false })
})
