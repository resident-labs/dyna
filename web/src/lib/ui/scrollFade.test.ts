import test from 'node:test'
import assert from 'node:assert/strict'
import { fadeSides } from './scrollFade.ts'

// A tab strip that scrolls sideways fades the edge where more content waits,
// and no edge when every tab fits.
test('a strip fades the edges that hide content', () => {
  const box = (scrollLeft: number, scrollWidth = 400, clientWidth = 200) => ({ scrollLeft, scrollWidth, clientWidth })
  assert.deepEqual(fadeSides(box(0)), { start: false, end: true }, 'the start hides nothing at the left end')
  assert.deepEqual(fadeSides(box(100)), { start: true, end: true }, 'both edges hide content in the middle')
  assert.deepEqual(fadeSides(box(200)), { start: true, end: false }, 'the end hides nothing at the right end')
  assert.deepEqual(fadeSides(box(0, 200, 200)), { start: false, end: false }, 'a strip that fits fades nothing')
  assert.deepEqual(fadeSides(box(0, 200.5, 200)), { start: false, end: false }, 'a rounded half pixel is not overflow')
})
