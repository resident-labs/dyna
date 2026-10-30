import test from 'node:test'
import assert from 'node:assert/strict'
import type { Goal } from '../api/goal.ts'
import { goalTree, goalStatus, goalTone, goalParts } from './goals.ts'

const goal = (id: string, parent = '', state = 'active'): Goal & { parent: string } => ({ id, room: 'r', text: id, setter: 'a', holders: ['a'], shared: false, parent, state, createdAt: '2026-10-17T11:00:00Z', updatedAt: '2026-10-17T11:00:00Z', edits: null })
test('tree preserves ancestors, indents children, and accepts orphan or cyclic history', () => {
  const all = [goal('g1'), goal('g2', 'g1', 'done'), goal('g3', 'g2'), goal('g4', 'missing'), goal('g5', 'g6'), goal('g6', 'g5')]
  assert.deepEqual(goalTree(all).slice(0, 4).map((r) => [r.goal.id, r.depth, r.context]), [['g1', 0, false], ['g2', 1, true], ['g3', 2, false], ['g4', 0, false]])
  assert.equal(goalTree(all).length, 6)
  assert.deepEqual(goalTree(all, 'done').map((r) => r.goal.id), ['g1', 'g2'])
})
test('status names the state, its summary, or the last update', () => {
  const now = Date.parse('2026-10-17T12:00:00Z')
  const g = { ...goal('g1'), updatedAt: '2026-10-17T11:56:00Z' }
  assert.equal(goalStatus(g, now), 'active · updated 4 m ago')
  assert.equal(goalStatus({ ...g, state: 'done', done: { summary: 'Shipped' } }), 'done · Shipped')
  assert.equal(goalStatus({ ...g, state: 'dropped' }), 'dropped')
  assert.equal(goalTone({ ...g, state: 'done' }), 'sage')
  assert.equal(goalTone(g), 'gold')
})


test('goal text splits its done condition into its own line', () => {
  assert.deepEqual(goalParts('Fix the cache; done when the PR is merged'), { aim: 'Fix the cache', done: 'the PR is merged' })
  assert.deepEqual(goalParts('Replace dyna land. Done when all changes merge.'), { aim: 'Replace dyna land.', done: 'all changes merge.' })
  assert.deepEqual(goalParts('Show goals in the profile'), { aim: 'Show goals in the profile', done: '' })
})
