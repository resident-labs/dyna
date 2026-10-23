import test from 'node:test'
import assert from 'node:assert/strict'
import { resetLine } from './noticeTime.ts'

const HOUR = 3600000
const MINUTE = 60000

test('a reset minutes away reads in minutes', () => {
  assert.equal(resetLine(42 * MINUTE), 'resets in 42m')
  assert.equal(resetLine(10 * 1000), 'resets in 1m', 'a reset under a minute still reads')
  assert.equal(resetLine(0), 'resets now')
  assert.equal(resetLine(-5 * MINUTE), 'resets now')
})

test('a reset hours away reads in hours', () => {
  assert.equal(resetLine(3 * HOUR), 'resets in 3h')
  assert.equal(resetLine(23 * HOUR + 59 * MINUTE), 'resets in 23h')
})

// A window six days out must not read like one this evening: it keeps its
// days and the hours after them.
test('a reset more than a day away keeps its days', () => {
  assert.equal(resetLine(24 * HOUR), 'resets in 1d')
  assert.equal(resetLine(152 * HOUR), 'resets in 6d 8h')
  assert.equal(resetLine(152 * HOUR + 5 * MINUTE), 'resets in 6d 8h', 'a few drifted minutes keep the same reading')
  assert.equal(resetLine(49 * HOUR + 30 * MINUTE), 'resets in 2d 1h')
  assert.equal(resetLine(Number.NaN), '')
})
