import test from 'node:test'
import assert from 'node:assert/strict'
import { expediteScope, heldGoals, expediteAction, isExpedited, none, prExpedited } from './expedite.ts'

const now = Date.parse('2026-10-22T12:00:00Z')
const goal = (id: string, holders: string[], extra: { expediteUntil?: string } = {}) => ({ id, state: 'active', holders, ...extra })
const a = { id: 'a', address: 'r:a' }
const c = { id: 'c', address: 'r:c', parent: 'b' }
const d = { id: 'd', address: 'r:d' }
const members = [a, { id: 'b', address: 'r:b', parent: 'a' }, c, d]

test('the scope covers the holder, its descendants and linked PRs, and ends at the deadline', () => {
  const goals = [goal('g1', ['r:a'], { expediteUntil: '2026-10-23T12:00:00Z' }), goal('g2', ['d'])]
  const scope = expediteScope(goals, members, now)
  assert.equal(scope.goalId, 'g1')
  assert.deepEqual([...scope.agents].sort(), ['a', 'b', 'c'])
  assert.equal(prExpedited(scope, { goalId: 'g1' }), true)
  assert.equal(prExpedited(scope, { goalId: 'g2' }), false)
  // At the deadline, and for an unset zero time or a finished goal, nothing carries the badge.
  assert.equal(expediteScope(goals, members, Date.parse('2026-10-23T12:00:00Z')), none)
  assert.equal(isExpedited(goal('g3', [], { expediteUntil: '0001-01-01T00:00:00Z' }), now), false)
  assert.equal(isExpedited({ ...goal('g1', ['r:a'], { expediteUntil: '2026-10-23T12:00:00Z' }), state: 'done' }, now), false)
})

test('an agent without a goal uses its nearest ancestor with one; several goals stay separate', () => {
  const goals = [goal('g1', ['r:a']), goal('g2', ['a']), goal('g3', ['d'])]
  assert.deepEqual(heldGoals(c, members, goals).map((g) => g.id), ['g1', 'g2'])
  assert.deepEqual(heldGoals(d, members, goals).map((g) => g.id), ['g3'])
  assert.deepEqual(heldGoals(d, members, [goal('g4', ['a'])]), [])
})

test('the button stops its own goal and replaces another', () => {
  const scope = expediteScope([goal('g1', ['a'], { expediteUntil: '2026-10-23T12:00:00Z' })], members, now)
  assert.equal(expediteAction(scope, { id: 'g1' }), 'stop')
  assert.equal(expediteAction(scope, { id: 'g2' }), 'replace')
  assert.equal(expediteAction(none, { id: 'g2' }), 'start')
})

test('linking an untracked PR first creates its owner record; tracked changes keep their key', async () => {
  const { readFileSync } = await import('node:fs')
  const { runFunctions, call } = await import('../../../test/svelte-source.ts')
  const calls: unknown[][] = []
  const c = {
    roomId: 'r',
    api: {
      trackPR: async (...args: unknown[]) => { calls.push(['track', ...args]); return { key: 'tracked' } },
      setPRGoal: async (...args: unknown[]) => { calls.push(['link', ...args]) },
    },
    loadChanges: async () => {},
  }
  runFunctions(readFileSync(new URL('../../App.svelte', import.meta.url), 'utf8'), ['linkPRGoal'], c)
  await call(c, 'linkPRGoal', { number: 42 }, 'g1')
  await call(c, 'linkPRGoal', { key: 'existing', number: 43 }, 'g2')
  assert.deepEqual(calls, [['track', 'r', '', 42], ['link', 'r', 'tracked', 'g1'], ['link', 'r', 'existing', 'g2']])
})
