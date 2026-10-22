import test from 'node:test'
import assert from 'node:assert/strict'
import { hiddenConnectors } from './chain.ts'

test('a connector that crosses a wrap is hidden', () => {
  // Three pills on the first row, two on the second: only the connector
  // between the third and fourth pills points at nothing.
  assert.deepEqual(hiddenConnectors([0, 0, 0, 40, 40]), [2])
  // Every pill on its own row: every connector crosses a wrap.
  assert.deepEqual(hiddenConnectors([0, 40, 80]), [0, 1])
})

test('one row keeps every connector', () => {
  assert.deepEqual(hiddenConnectors([0, 0, 0]), [])
  assert.deepEqual(hiddenConnectors([12, 12]), [])
})

test('no connectors come back for fewer than two pills', () => {
  assert.deepEqual(hiddenConnectors([]), [])
  assert.deepEqual(hiddenConnectors([0]), [])
})
