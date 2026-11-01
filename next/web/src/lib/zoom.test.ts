import test from 'node:test'
import assert from 'node:assert/strict'
import { FIT, MAX_SCALE, actualScale, clampView, toggleZoom, zoomAt } from './zoom.ts'

const box = { width: 400, height: 200 }

test('clampView limits scale and keeps the image covering the box', () => {
  assert.equal(clampView({ scale: 99, x: 0, y: 0 }, box).scale, MAX_SCALE)
  assert.deepEqual(clampView({ scale: 0.2, x: 50, y: 50 }, box), FIT)
  assert.deepEqual(clampView({ scale: 2, x: 999, y: -999 }, box), { scale: 2, x: 200, y: -100 })
})

test('zoomAt keeps the content under the cursor fixed', () => {
  const at = { x: 100, y: 40 }
  const next = zoomAt(FIT, 2, at, box)
  assert.deepEqual(next, { scale: 2, x: -100, y: -40 })
  // Screen position of the content point that was at `at`: x + scale * at.
  assert.equal(next.x + next.scale * at.x, at.x)
})

test('a small image cannot be panned until it overflows the box', () => {
  const content = { width: 100, height: 100 }
  assert.deepEqual(clampView({ scale: 2, x: 50, y: 50 }, box, content), { scale: 2, x: 0, y: 0 })
  assert.deepEqual(clampView({ scale: 6, x: 999, y: 0 }, box, content), { scale: 6, x: 100, y: 0 })
})

test('actualScale gives 100% when the image is shrunk to fit, else 2x', () => {
  assert.equal(actualScale(0.5), 2)
  assert.equal(actualScale(0.66), 1 / 0.66)
  assert.equal(actualScale(1), 2)
})

test('toggleZoom switches between fit and 2x', () => {
  const zoomed = toggleZoom(FIT, { x: 0, y: 0 }, box)
  assert.equal(zoomed.scale, 2)
  assert.deepEqual(toggleZoom(zoomed, { x: 0, y: 0 }, box), FIT)
})
