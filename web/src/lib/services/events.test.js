import test from 'node:test'
import assert from 'node:assert/strict'
import { serviceEvents, serviceSessions } from './events.js'

const at = (min) => new Date(Date.UTC(2026, 9, 20, 1, min)).toISOString()
const members = [
  { owner: true, name: 'repo' },
  { id: 's1', name: 'lead', address: 'work:s1', role: '', created: at(10), state: 'live', status: 'running', lastActivity: at(50) },
  { id: 's2', name: 'reviewer-1162', role: 'reviewer', service: true, created: at(20), state: 'idle', status: 'idle', lastActivity: at(40) },
  { id: 's3', name: 'old', created: at(1), status: 'closed', closed: true, ended: at(30), state: 'closed', lastActivity: at(30) },
  { id: 's4', name: 'napper', created: at(2), status: 'dormant', finished: true, ended: at(35), state: 'idle', lastActivity: at(35) },
  { id: 's5', name: 'keeper', role: 'keeper', service: true, created: at(3), state: 'idle', status: 'idle', lastActivity: at(45) },
]
const changes = [{ number: 1162, title: 'Add the page', reviewerSession: 's2', reviewerName: 'reviewer-1162', verdict: 'approve-with-nits', reviewAt: at(42) }, { number: 9, reviewerSession: 's2' }]
const journal2 = [
  { id: 'e3', at: at(47), goal: 'g2', by: 'work:s1', event: 'assigned', text: 'i1 to me' },
]
const journal = [
  { id: 'e2', at: at(50), goal: 'g1', by: 's1', event: 'note', text: 'holder note' },
  { id: 'e1', at: at(45), goal: 'g1', by: 'keeper', event: 'keeper-status', text: 'lead on_track; napper waiting, needs action' },
]

test('events are newest first and each kind has its own line', () => {
  const events = serviceEvents({ members, changes, journal })
  assert.deepEqual(events.map((e) => [e.who, e.text]), [
    ['lead', 'note g1: holder note'],
    ['keeper', 'keeper-status g1: lead on_track; napper waiting, needs action'],
    ['reviewer-1162', 'gave a verdict: approve with nits on #1162'],
    ['napper', 'finished'],
    ['old', 'closed'],
    ['reviewer-1162', 'started on #1162'],
    ['lead', 'started'],
    ['keeper', 'started'],
    ['napper', 'started'],
    ['old', 'started'],
  ])
  for (let i = 1; i < events.length; i++) assert.ok(events[i - 1].ts >= events[i].ts)
})

test('an event names the session it opens', () => {
  const events = serviceEvents({ members, changes, journal })
  assert.equal(events.find((e) => e.text.startsWith('gave a verdict')).session, 's2')
  assert.equal(events.find((e) => e.text.startsWith('note g1')).session, 's1')
  assert.equal(events.find((e) => e.text.startsWith('keeper-status')).session, undefined, 'the keeper sender is no session')
  assert.equal(events.find((e) => e.who === 'old' && e.text === 'closed').session, 's3')
})

test('a change with no review time, and the owner, make no event', () => {
  const events = serviceEvents({ members, changes: [{ number: 9, verdict: 'approve' }], journal: [] })
  assert.ok(!events.some((e) => e.text.includes('verdict') || e.who === 'repo'))
})

test('sessions split into current and earlier; earlier is newest ended first', () => {
  const { current, earlier } = serviceSessions(members)
  assert.deepEqual(current.map((m) => m.name), ['lead', 'keeper', 'reviewer-1162', 'napper'])
  assert.deepEqual(earlier.map((m) => m.name), ['old'])
})

test('merge trial and publish results are events, tagged with their service', () => {
  const history = [{ kind: 'publish', outcome: 'published', at: at(48), changes: 3, detail: '' }, { kind: 'trial', outcome: 'failed', at: at(44), changes: 2, detail: 'go-test' }]
  const events = serviceEvents({ members, changes, journal, history })
  const merge = events.filter((e) => e.service === 'merge')
  assert.deepEqual(merge.map((e) => e.text), ['published main · 3 changes', 'trial failed: go-test'])
  assert.equal(events.find((e) => e.who === 'reviewer-1162' && e.text.startsWith('gave'))?.service, 'reviewer')
  assert.equal(events.find((e) => e.who === 'keeper' && e.text.startsWith('keeper-status'))?.service, 'keeper')
  assert.equal(events.find((e) => e.who === 'lead' && e.text === 'started')?.service, 'agents')
})

test('an event by session address resolves to the member', () => {
  const events = serviceEvents({ members, changes: [], journal: journal2 })
  const e = events.find((x) => x.text.startsWith('assigned'))
  assert.equal(e.who, 'lead')
  assert.equal(e.session, 's1')
})
