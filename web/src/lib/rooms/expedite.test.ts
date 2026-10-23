import test from 'node:test'
import assert from 'node:assert/strict'
import { expediteScope, heldGoals, expediteAction, isExpedited, none, prExpedited, goalExpedited } from './expedite.ts'

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

test('items narrow the scope to their assignees, and root never hands the badge to every child', () => {
  const root = { id: 'root', address: 'r:root', role: 'root' }
  const lead = { id: 'lead', address: 'r:lead', parent: 'root' }
  const worker = { id: 'worker', address: 'r:worker', parent: 'lead' }
  const other = { id: 'other', address: 'r:other', parent: 'root' }
  const tree = [root, lead, worker, other]
  const until = { expediteUntil: '2026-10-23T12:00:00Z' }
  // Root holds the goal and every session is its child. A goal with items goes
  // to the item assignees, their children and the linked PRs, not to root or
  // to root's other children.
  const held = {
    ...goal('g1', ['r:root'], until),
    items: [{ id: 'i1', state: 'open', assignee: 'lead' }, { id: 'i2', state: 'done', assignee: 'other' }],
  }
  const scope = expediteScope([held], tree, now)
  assert.deepEqual([...scope.agents].sort(), ['lead', 'worker'])
  assert.equal(prExpedited(scope, { goalId: 'g1' }), true)
  // A goal without items covers the holder and its children...
  assert.deepEqual([...expediteScope([goal('g2', ['r:lead'], until)], tree, now).agents].sort(), ['lead', 'worker'])
  // ...but a root or owner holder carries no badge, and its children never do.
  assert.deepEqual([...expediteScope([goal('g3', ['r:root'], until)], tree, now).agents], [])
  assert.deepEqual([...expediteScope([goal('g4', ['r:owner'], until)], [{ id: 'owner', address: 'r:owner', owner: true }, ...tree], now).agents], [])
  // A Room goal has no holder: a shared goal with an item covers the assignee.
  const shared = { ...goal('g5', [], until), shared: true, items: [{ id: 'i1', state: 'open', assignee: 'r:other' }] }
  assert.deepEqual([...expediteScope([shared], tree, now).agents], ['other'])
  // An item assigned to root itself does not spread to root's children.
  const byRoot = { ...goal('g6', [], until), shared: true, items: [{ id: 'i1', state: 'open', assignee: 'r:root' }] }
  assert.deepEqual([...expediteScope([byRoot], tree, now).agents], ['root'])
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


test('link and expedite preserves current priority while the separate stop action still toggles it', async () => {
  const { readFileSync } = await import('node:fs')
  const { runFunctions, call } = await import('../../../test/svelte-source.ts')
  const g = goal('g1', ['a'], { expediteUntil: '2026-10-23T12:00:00Z' })
  let links = 0
  const c = {
    goals: [g], change: { key: 'pr' }, $expedite: expediteScope([g], members, now), goalExpedited,
    xpBusy: false, xpError: '', linking: true,
    onlinkgoal: async () => { links++ },
    onexpedite: async () => { c.$expedite = none },
  }
  runFunctions(readFileSync(new URL('../prs/PRDetail.svelte', import.meta.url), 'utf8'), ['expediteChange'], c)
  await call(c, 'expediteChange', undefined, 'g1')
  assert.equal(links, 1)
  assert.equal(c.$expedite.goalId, 'g1', 'linking to the current expedited goal must preserve its priority')
  assert.equal(c.xpError, '')
  assert.equal(c.linking, false)
  await call(c, 'expediteChange', g)
  assert.equal(c.$expedite, none, 'the separate Stop expediting action must still stop priority')
})
