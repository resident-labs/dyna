import test from 'node:test'
import assert from 'node:assert/strict'
import { ancestors, finishedTrees, foldMembers, isFinished, openTrail, orderMemberSessions, type MemberView } from './memberOrder.ts'
import { channelModel } from './channels.ts'

const now = Date.parse('2026-10-17T17:00:00Z')
const minute = 60 * 1000
type Fixture = MemberView & { id: string; name: string; parent: string; depth: number; state: string; closed: boolean; lastActivity: string }
const member = (id: string, parent = '', overrides: Partial<Fixture> = {}): Fixture => ({ id, name: id, parent, depth: 0, state: 'idle', closed: false, lastActivity: new Date(now - minute).toISOString(), ...overrides })
const finished: Partial<Fixture> = { status: 'dormant' }
const ids = (members: readonly { id: string }[]): string[] => members.map((m) => m.id)

test('finished siblings move last at every level with their complete subtrees', () => {
  const sessions = [
    member('finished-lead', '', finished),
    member('finished-lead-live-child', 'finished-lead', { depth: 1, state: 'live' }),
    member('active-lead'),
    member('finished-child-a', 'active-lead', { depth: 1, ...finished }),
    member('live-grandchild', 'finished-child-a', { depth: 2, state: 'live' }),
    member('active-child-a', 'active-lead', { depth: 1 }),
    member('finished-grandchild', 'active-child-a', { depth: 2, ...finished }),
    member('active-great-grandchild', 'finished-grandchild', { depth: 3 }),
    member('active-grandchild', 'active-child-a', { depth: 2 }),
    member('active-child-b', 'active-lead', { depth: 1 }),
    member('finished-child-b', 'active-lead', { depth: 1, ...finished }),
    member('second-active-lead'),
    member('second-finished-lead', '', finished),
  ]
  const before = structuredClone(sessions)
  const ordered = orderMemberSessions(sessions)
  assert.deepEqual(ids(ordered), [
    'active-lead',
    'active-child-a', 'active-grandchild', 'finished-grandchild', 'active-great-grandchild',
    'active-child-b',
    'finished-child-a', 'live-grandchild', 'finished-child-b',
    'second-active-lead',
    'finished-lead', 'finished-lead-live-child',
    'second-finished-lead',
  ])
  assert.deepEqual(sessions, before)
  for (const row of ordered) assert.deepEqual(row, sessions.find((m) => m.id === row.id))
})

test('closed sessions retain their group and omitted parents leave visible roots', () => {
  const sessions = [
    member('finished', '', finished),
    member('closed', '', { closed: true, state: 'closed', ...finished }),
    member('closed-child', 'closed', { depth: 1 }),
    member('orphan', 'hidden-closed-parent', { depth: 2 }),
    member('orphan-finished-child', 'orphan', { depth: 1, ...finished }),
    member('orphan-active-child', 'orphan', { depth: 1 }),
  ]
  assert.deepEqual(ids(orderMemberSessions(sessions)), [
    'closed', 'closed-child', 'orphan', 'orphan-active-child', 'orphan-finished-child', 'finished',
  ])
  assert.deepEqual(orderMemberSessions([]), [])
  const shown = new Map(orderMemberSessions(sessions).map((m): [string, number] => [m.id, m.depth]))
  assert.equal(shown.get('orphan'), 0, 'an orphan shows at the top level')
  assert.equal(shown.get('orphan-active-child'), 1)
})

test('folded sessions hide their subtrees and count hidden live work', () => {
  const sessions = [
    member('lead-a'),
    member('child-a1', 'lead-a', { depth: 1, state: 'live' }),
    member('grandchild', 'child-a1', { depth: 2, state: 'live' }),
    member('child-a2', 'lead-a', { depth: 1 }),
    member('lead-b'),
    member('child-b1', 'lead-b', { depth: 1 }),
    member('grandchild-b', 'child-b1', { depth: 2, state: 'live' }),
    member('solo'),
  ]
  const closed = foldMembers(sessions, () => false)
  assert.deepEqual(closed.map((row) => row.member.id), ['lead-a', 'lead-b', 'solo'])
  assert.deepEqual(closed.map((row) => [row.folded, row.hidden]), [[true, 3], [true, 2], [false, 0]])

  const open = openTrail(sessions, 'child-b1')
  assert.deepEqual([...open], ['lead-b', 'child-b1'])
  const focused = foldMembers(sessions, (id) => open.has(id))
  assert.deepEqual(focused.map((row) => row.member.id), ['lead-a', 'lead-b', 'child-b1', 'grandchild-b', 'solo'])
  assert.equal(focused.find((row) => row.member.id === 'child-b1')?.folded, false)

  // An open child under a folded parent stays hidden with its subtree.
  const deep = foldMembers(sessions, (id) => id === 'child-a1')
  assert.deepEqual(deep.map((row) => row.member.id), ['lead-a', 'lead-b', 'solo'])
  assert.equal(deep[0]?.hidden, 3)
  assert.equal(foldMembers(sessions, () => true).length, sessions.length)
})

test('finished sessions sort after open siblings and form finishedTrees when no open session remains', () => {
  const sessions = [
    member('done-lead', '', { state: 'done', status: 'dormant' }),
    member('done-child', 'done-lead', { depth: 1, closed: true, state: 'closed' }),
    member('other-lead'),
    member('active-lead'),
    member('mixed-done', '', { status: 'dormant', state: 'done' }),
    member('mixed-open-child', 'mixed-done', { depth: 1, state: 'live' }),
    member('closed-only', '', { closed: true, state: 'closed' }),
  ]
  assert.equal(isFinished(sessions[0]), true)
  assert.equal(isFinished(sessions[1]), false)
  assert.equal(isFinished(sessions[2]), false)
  assert.equal(isFinished({ status: 'dormant', closed: true }), false)
  const ordered = orderMemberSessions(sessions)
  assert.deepEqual(ids(ordered), [
    'other-lead',
    'active-lead',
    'closed-only',
    'done-lead',
    'done-child',
    'mixed-done',
    'mixed-open-child',
  ])
  assert.deepEqual([...finishedTrees(ordered)], ['done-lead', 'done-child'])
})

test('channels and the member list walk the same ancestors, and a parent loop stops', () => {
  const sessions = [member('lead'), member('child', 'lead'), member('grandchild', 'child'), member('a', 'b'), member('b', 'a')]
  const byId = new Map<string | undefined, Fixture>(sessions.map((m): [string, Fixture] => [m.id, m]))
  assert.deepEqual(ancestors(byId, 'grandchild').map((m) => m.id), ['lead', 'child', 'grandchild'])
  assert.deepEqual(ancestors(byId, 'a').map((m) => m.id), ['b', 'a'])
  assert.deepEqual(ancestors(byId, 'missing'), [])
  assert.deepEqual(channelModel(sessions).trail('grandchild').map((m) => m.id), [...openTrail(sessions, 'grandchild')])
})

test('dormant status alone groups a finished session', () => {
  assert.equal(isFinished({ status: 'dormant' }), true)
  const idle = { status: 'idle', finished: true }
  assert.equal(isFinished(idle), false)
})
