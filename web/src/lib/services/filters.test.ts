import assert from 'node:assert/strict'
import test from 'node:test'
import { defaultFilters, filterEvents, filterSessions } from './filters.ts'

const now = Date.parse('2026-10-20T12:00:00Z')
const events = [
  { ts: now - 60000, service: 'reviewer', id: 'a' },
  { ts: now - 2 * 3600000, service: 'agents', id: 'b' },
  { ts: now - 3 * 86400000, service: 'merge', id: 'c' },
]

test('events filter by service and by time range', () => {
  assert.deepEqual(filterEvents(events, defaultFilters, now).map((e) => e.id), ['a', 'b'])
  assert.deepEqual(filterEvents(events, { ...defaultFilters, range: 'hour' }, now).map((e) => e.id), ['a'])
  assert.deepEqual(filterEvents(events, { ...defaultFilters, range: 'all' }, now).map((e) => e.id), ['a', 'b', 'c'])
  assert.deepEqual(filterEvents(events, { ...defaultFilters, range: 'all', service: 'merge' }, now).map((e) => e.id), ['c'])
})

test('sessions filter by service and by state', () => {
  const sessions = [
    { id: 1, role: 'reviewer', state: 'live' },
    { id: 2, role: '', state: 'idle' },
    { id: 3, state: 'idle', closed: true },
    { id: 4, state: 'live' },
  ]
  const ids = (f: Partial<typeof defaultFilters>) => filterSessions(sessions, { ...defaultFilters, ...f }).map((m) => m.id)
  assert.deepEqual(ids({}), [1, 2, 3, 4])
  assert.deepEqual(ids({ state: 'running' }), [1, 4])
  assert.deepEqual(ids({ state: 'closed' }), [3])
  assert.deepEqual(ids({ service: 'agents' }), [2, 3, 4])
  assert.deepEqual(ids({ service: 'reviewer', state: 'idle' }), [])
})
