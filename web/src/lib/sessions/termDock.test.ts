import { test } from 'node:test'
import assert from 'node:assert/strict'
import { clampHeight, clampRect, closedDock, dockOf, dockReduce, legacyView, placeNew } from './termDock.ts'

test('the dock height stays between 120 px and 80% of the main area', () => {
  assert.equal(clampHeight(40, 800), 120)
  assert.equal(clampHeight(500, 800), 500)
  assert.equal(clampHeight(900, 800), 640)
  assert.equal(clampHeight(500, 100), 120) // a tiny main area still gets the minimum
  assert.equal(clampHeight(NaN, 800), 280)
})

test('hide drops maximize but keeps height and tabs', () => {
  let v = dockReduce(closedDock(), { type: 'resize', height: 400, avail: 1000 })
  v = dockReduce(dockReduce(v, { type: 'tab', ref: 'r', id: 't1' }), { type: 'maximize' })
  assert.deepEqual([v.open, v.maxed], [true, true])
  v = dockReduce(v, { type: 'hide' })
  assert.deepEqual([v.open, v.maxed, v.height, v.active], [false, false, 400, { r: 't1' }])
  assert.equal(dockReduce(v, { type: 'show' }).open, true)
})

test('a saved dock is read back and broken values fall back to closed', () => {
  assert.deepEqual(dockOf({ open: true, height: 9999, maxed: true, active: { r: 't1', x: 3 } }), { open: true, height: 9999, maxed: true, active: { r: 't1' }, wins: [] })
  assert.deepEqual(dockOf('nope'), closedDock())
  assert.equal(dockOf({ open: false, maxed: true }).maxed, false)
})

test('the old terminal view opens the room feed with the dock open', () => {
  assert.deepEqual(legacyView('terminal', 'general'), { view: 'general', dock: true })
  assert.deepEqual(legacyView('library', 'general'), { view: 'library', dock: false })
})

test('a window stays inside the main area and keeps its minimum size', () => {
  const area = { w: 1000, h: 600 }
  assert.deepEqual(clampRect({ x: 900, y: 590, w: 400, h: 300 }, area), { x: 600, y: 300, w: 400, h: 300 })
  assert.deepEqual(clampRect({ x: -50, y: -9, w: 5000, h: 5000 }, area), { x: 0, y: 0, w: 1000, h: 600 })
  assert.deepEqual(clampRect({ x: 10, y: 10, w: 20, h: 20 }, area), { x: 10, y: 10, w: 280, h: 160 })
  assert.deepEqual(clampRect({ x: 50, y: 50, w: 400, h: 300 }, { w: 100, h: 100 }), { x: 0, y: 0, w: 280, h: 160 })
  for (let n = 0; n < 20; n++) {
    const r = placeNew(area, n)
    assert.ok(r.x >= 0 && r.y >= 0 && r.x + r.w <= area.w && r.y + r.h <= area.h, `window ${n} fits`)
  }
})

test('popping out, raising, minimizing and docking a window', () => {
  const area = { w: 1000, h: 600 }
  let v = dockReduce(dockReduce(closedDock(), { type: 'pop', id: 'a', area }), { type: 'pop', id: 'b', area })
  assert.deepEqual(v.wins.map((w) => [w.id, w.z]), [['a', 1], ['b', 2]])
  v = dockReduce(v, { type: 'raise', id: 'a' })
  assert.deepEqual(v.wins.map((w) => [w.id, w.z]), [['a', 3], ['b', 2]])
  assert.equal(dockReduce(v, { type: 'raise', id: 'a' }), v, 'the top window raises to itself')
  v = dockReduce(dockReduce(v, { type: 'minimize', id: 'b' }), { type: 'pop', id: 'b', area })
  assert.deepEqual(v.wins.find((w) => w.id === 'b'), { ...v.wins[1], min: false, z: 4 }, 'popping a minimized shell restores it on top')
  v = dockReduce(v, { type: 'move', id: 'a', x: 5000, y: -5, area })
  const a = v.wins.find((w) => w.id === 'a')
  assert.ok(a && a.x + a.w === 1000 && a.y === 0)
  assert.deepEqual(dockReduce(v, { type: 'dockIn', id: 'a' }).wins.map((w) => w.id), ['b'])
  assert.deepEqual(dockReduce(v, { type: 'prune', keep: ['a'] }).wins.map((w) => w.id), ['a'])
})

test('saved windows are read back and broken ones are dropped', () => {
  const saved = { open: true, wins: [{ id: 'a', x: 1, y: 2, w: 300, h: 200, z: 4, min: true }, { id: 'a' }, { x: 1 }, 'x'] }
  assert.deepEqual(dockOf(saved).wins, [{ id: 'a', x: 1, y: 2, w: 300, h: 200, z: 4, min: true }])
  assert.deepEqual(dockOf({ wins: 'nope' }).wins, [])
})
