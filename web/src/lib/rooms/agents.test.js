import test from 'node:test'
import assert from 'node:assert/strict'
import { activeAgents, agentsHeader, roomsWorking } from './agents.js'

const now = Date.parse('2026-10-18T12:00:00Z')
const recent = new Date(now - 60 * 1000).toISOString()
const old = new Date(now - 60 * 60 * 1000).toISOString()
const agent = (name, extra = {}) => ({ id: name, name, address: `r:${name}`, state: 'idle', lastActivity: recent, ...extra })

test('lists awake agents by room and counts dormant and paused ones as hidden', () => {
  // Only finish makes an agent dormant; a long idle agent stays awake.
  const rooms = [{ id: 'a' }, { id: 'b', paused: true }, { id: 'c', archived: true }, { id: 'd' }]
  const members = {
    a: [{ owner: true, name: 'a' }, agent('lead', { state: 'live' }), agent('sleepy', { lastActivity: old }), agent('gone', { closed: true }), agent('over', { status: 'dormant' }), agent('keeper', { service: true })],
    b: [agent('held')],
    c: [agent('archived')],
    d: [agent('solo')],
  }
  const { groups, hidden } = activeAgents(rooms, members)
  assert.deepEqual(groups.map((g) => [g.room.id, g.members.map((m) => m.name)]), [['a', ['lead', 'sleepy']], ['d', ['solo']]])
  assert.equal(groups[0].members[0].roomId, 'a')
  assert.equal(hidden, 2)
  assert.equal(activeAgents(rooms, members).working, 1)
})

test('a live agent is never hidden', () => {
  const { groups } = activeAgents([{ id: 'a' }], { a: [agent('busy', { state: 'live', lastActivity: old })] })
  assert.equal(groups[0].members[0].name, 'busy')
})

test('before member lists load, the agents view is loading and keeps the rooms summary count', () => {
  const rooms = [{ id: 'a', running: 2, members: 3 }, { id: 'b', running: 1, members: 1 }, { id: 'c', running: 0, members: 0 }, { id: 'x', running: 4, members: 2, archived: true }]
  const view = activeAgents(rooms, null)
  assert.equal(view.loading, true)
  assert.equal(view.working, 3, 'the rail count, not 0')
  assert.deepEqual(view.groups, [])
  assert.equal(activeAgents(rooms, {}).loading, false, 'an answer, even an empty one, is final')
})

test('the Home header never waits on member lists: it shows the rail count until they load', () => {
  const rooms = [{ id: 'a', running: 2, members: 3 }, { id: 'b', running: 1, members: 1 }, { id: 'p', running: 5, members: 2, paused: true }]
  // The sidebar is hidden (a phone drawer that closed, or a hidden desktop
  // sidebar), so member lists never load; the header still has a final text.
  assert.deepEqual(agentsHeader(activeAgents(rooms, null)), { rooms: '', working: 3 })
  assert.equal(roomsWorking(rooms), 3, 'one count for the rail and the loading view')
  const loaded = activeAgents([{ id: 'a' }], { a: [{ id: 'm', state: 'live' }] })
  assert.deepEqual(agentsHeader(loaded), { rooms: '1 room with agents', working: 1 })
})
