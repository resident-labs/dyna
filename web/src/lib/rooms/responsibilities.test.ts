import test from 'node:test'
import assert from 'node:assert/strict'
import { linesOf, summaryOf, totalOf } from './responsibilities.ts'

test('the total is the sum of every kind', () => {
  assert.equal(totalOf({ pr: 1, goal: 1, shell: 3 }), 5)
  assert.equal(totalOf({}), 0)
})

test('lines come in a fixed order, skip zero counts and use singular or plural labels', () => {
  assert.deepEqual(linesOf({ shell: 2, goal: 1, child: 0, pr: 1 }), [
    { kind: 'pr', n: 1, label: 'pull request' },
    { kind: 'goal', n: 1, label: 'goal' },
    { kind: 'shell', n: 2, label: 'shells' },
  ])
})

test('an assigned goal item is one of the kinds', () => {
  assert.deepEqual(linesOf({ item: 2 }), [{ kind: 'item', n: 2, label: 'goal items' }])
  assert.equal(summaryOf({ goal: 1, item: 1 }), '1 goal, 1 goal item')
})

test('a kind the web does not know still counts, after the known kinds', () => {
  assert.deepEqual(linesOf({ zeta: 1, goal: 2 }).map((l) => l.kind), ['goal', 'zeta'])
  assert.equal(totalOf({ zeta: 1, goal: 2 }), 3)
})

test('missing or malformed counts give nothing', () => {
  for (const value of [undefined, null, 'x', 4, { goal: 'many' }]) {
    assert.deepEqual(linesOf(value), [])
    assert.equal(totalOf(value), 0)
  }
})

test('the summary is one line of counts', () => {
  assert.equal(summaryOf({ goal: 1, shell: 2 }), '1 goal, 2 shells')
  assert.equal(summaryOf(undefined), '')
})
