import { test } from 'node:test'
import assert from 'node:assert/strict'
import { actions, actionItems, roomMenu } from './actions.ts'

function context(room = { id: 'room_test', name: 'test', repo: '/tmp/repo' }) {
  const calls = []
  const context = { room, rooms: [room], icons: { pin: 'pin-icon', archive: 'archive-icon' }, hasTerminal: true,
    roomAction: async (run, note) => { calls.push(note); await run() },
  }
  for (const name of ['switchRoom', 'newRoom', 'openFiles', 'openView', 'openSettings', 'startWorkflow', 'openPins', 'openFind', 'openRoomSettings', 'openSessionPRs', 'cancelRun', 'renameRoom', 'deleteRoom', 'say', 'loadRooms', 'toggleSidebar']) context[name] = (...args) => calls.push({ name, args })
  return { context, calls }
}

test('shared surfaces use one handler per action with stable IDs', () => {
  const { context: c, calls } = context()
  const registry = actions(c)
  const palette = actionItems(registry, 'palette'), slash = actionItems(registry, 'slash'), menu = roomMenu(registry)
  for (const id of ['rename', 'room-pin', 'room-archive']) {
    const handler = registry.find((a) => a.id === id).run
    assert.equal(palette.find((a) => a.id === id).run, handler)
    assert.equal(slash.find((a) => a.id === id).run, handler)
    assert.equal(menu.find((a) => a.id === id).run, handler)
  }
  palette.find((a) => a.id === 'files').run()
  slash.find((a) => a.name === 'files').run('src/file.js')
  assert.deepEqual(calls, [{ name: 'openFiles', args: [] }, { name: 'openFiles', args: ['src/file.js'] }])
  assert.equal(new Set(registry.map((a) => a.id)).size, registry.length)
})

test('the room menu opens Room settings; delete stays in the palette only', () => {
  const { context: c, calls } = context()
  const registry = actions(c)
  roomMenu(registry).find((a) => a.id === 'roomsettings').run()
  assert.deepEqual(calls, [{ name: 'openRoomSettings', args: [c.room] }])
  assert.equal(actionItems(registry, 'palette').find((a) => a.id === 'roomsettings').label, 'Room settings…')
  assert.equal(roomMenu(registry).some((a) => a.id === 'delete' || a.id === 'prsettings'), false)
  assert.equal(actionItems(registry, 'palette').some((a) => a.id === 'delete'), true)
})

test('Find in this room is one action in the palette and as /find with its text', () => {
  const { context: c, calls } = context()
  const registry = actions(c)
  actionItems(registry, 'palette').find((a) => a.id === 'find').run()
  actionItems(registry, 'slash').find((a) => a.name === 'find').run('deploy failed')
  assert.deepEqual(calls, [{ name: 'openFind', args: [] }, { name: 'openFind', args: ['deploy failed'] }])
})

test('there is no room Changes command', () => {
  const { context: c } = context({ id: 'room_test', name: 'test', repo: '/repo' })
  c.hasTerminal = true
  for (const surface of ['palette', 'slash']) assert.equal(actionItems(actions(c), surface).some((a) => a.id === 'changes'), false)
})

test('availability matches across palette and slash input', () => {
  const { context: c } = context({ id: 'room_test', name: 'test', repo: '' })
  c.hasTerminal = false
  const registry = actions(c)
  for (const surface of ['palette', 'slash']) {
    const ids = actionItems(registry, surface).map((a) => a.id)
    for (const id of ['files', 'github', 'issues', 'terminal']) assert.equal(ids.includes(id), false)
    for (const id of ['jobs', 'run', 'workflows']) assert.equal(ids.includes(id), true)
  }
  c.room = null
  assert.equal(actionItems(actions(c), 'slash').length, 0)
})

test('room toggles preserve IDs, icons, order, and shared pin calculation', async () => {
  const { context: c, calls } = context()
  const sent = []
  const names = { PinRoom: 'pin', UnpinRoom: 'unpin', ArchiveRoom: 'archive', UnarchiveRoom: 'unarchive' }
  globalThis.fetch = async (path, options) => {
    sent.push([names[path.slice('/api/'.length)], ...JSON.parse(options.body)])
    return Response.json(null)
  }
  c.rooms.push({ pinOrder: 4 })
  for (const pinned of [false, true]) {
    c.room.pinned = pinned
    c.room.archived = pinned
    const registry = actions(c)
    const menu = roomMenu(registry)
    assert.deepEqual(menu.map((a) => a.id), ['rename', 'room-pin', 'room-archive', 'roomsettings'])
    const palette = actionItems(registry, 'palette'), slash = actionItems(registry, 'slash')
    assert.equal(palette.find((a) => a.id === 'room-archive').icon, 'archive-icon')
    assert.equal(slash.find((a) => a.id === 'room-pin').name, pinned ? 'unpin' : 'pin')
    await menu.find((a) => a.id === 'room-pin').run()
    await menu.find((a) => a.id === 'room-archive').run()
  }
  assert.deepEqual(sent, [['pin', 'room_test', 5], ['archive', 'room_test'], ['unpin', 'room_test'], ['unarchive', 'room_test']])
  assert.equal(calls[0], 'test is pinned to the rail.')
})

test('the issues action opens the Issues tab and the prs action opens Pull requests', () => {
  const { context: c, calls } = context()
  const slash = actionItems(actions(c), 'slash')
  slash.find((a) => a.name === 'issues').run()
  slash.find((a) => a.name === 'prs').run()
  assert.deepEqual(calls, [{ name: 'openView', args: ['issues'] }, { name: 'openView', args: ['github'] }])
})

test('settings actions are available to every web client', () => {
  const { context: c, calls } = context()
  const registry = actions(c)
  for (const id of ['pair', 'devices', 'settings']) {
    const action = actionItems(registry, 'palette').find((a) => a.id === id)
    assert.ok(action)
    action.run()
  }
  assert.deepEqual(calls, [
    { name: 'openSettings', args: ['devices', true] },
    { name: 'openSettings', args: ['devices'] },
    { name: 'openSettings', args: [] },
  ])
})
