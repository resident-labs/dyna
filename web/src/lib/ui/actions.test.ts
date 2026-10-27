import { test } from 'node:test'
import assert from 'node:assert/strict'
import { actions, actionItems, roomMenu } from './actions.ts'
import type { ActionContext, ActionRoom } from './actions.ts'
import { need } from '../../../test/need.ts'

interface TestRoom extends ActionRoom {
  id: string
  name: string
  repo?: string
  pinned?: boolean
  archived?: boolean
}

interface TestContext extends ActionContext {
  room: TestRoom | null
  rooms: { pinOrder?: number }[]
}

// The registry always carries the actions these tests name.
function pick<T extends { id: string }>(items: readonly T[], id: string): T {
  return need(items.find((item) => item.id === id), id)
}

function pickName<T extends { name?: string }>(items: readonly T[], name: string): T {
  return need(items.find((item) => item.name === name), name)
}

function context(room: TestRoom = { id: 'room_test', name: 'test', repo: '/tmp/repo' }) {
  const calls: unknown[] = []
  const handler = (name: string) => (...args: unknown[]) => calls.push({ name, args })
  const context: TestContext = { room, rooms: [room], icons: { pin: 'pin-icon', archive: 'archive-icon' }, hasTerminal: true,
    roomAction: async (run: () => Promise<unknown>, note: string) => { calls.push(note); await run() },
    switchRoom: handler('switchRoom'),
    newRoom: handler('newRoom'),
    openFiles: handler('openFiles'),
    openView: handler('openView'),
    openSettings: handler('openSettings'),
    startWorkflow: handler('startWorkflow'),
    openPins: handler('openPins'),
    openSearch: handler('openSearch'),
    openRoomSettings: handler('openRoomSettings'),
    openSessionPRs: handler('openSessionPRs'),
    cancelRun: handler('cancelRun'),
    renameRoom: handler('renameRoom'),
    deleteRoom: handler('deleteRoom'),
    say: handler('say'),
    loadRooms: handler('loadRooms'),
    toggleSidebar: handler('toggleSidebar'),
  }
  return { context, calls, room }
}

test('shared surfaces use one handler per action with stable IDs', () => {
  const { context: c, calls } = context()
  const registry = actions(c)
  const palette = actionItems(registry, 'palette'), slash = actionItems(registry, 'slash'), menu = roomMenu(registry)
  for (const id of ['rename', 'room-pin', 'room-archive']) {
    const handler = pick(registry, id).run
    assert.equal(pick(palette, id).run, handler)
    assert.equal(pick(slash, id).run, handler)
    assert.equal(pick(menu, id).run, handler)
  }
  pick(palette, 'files').run()
  pick(slash, 'files').run('src/file.js')
  assert.deepEqual(calls, [{ name: 'openFiles', args: [] }, { name: 'openFiles', args: ['src/file.js'] }])
  assert.equal(new Set(registry.map((a) => a.id)).size, registry.length)
})

test('the room menu opens Room settings; delete stays in the palette only', () => {
  const { context: c, calls } = context()
  const registry = actions(c)
  pick(roomMenu(registry), 'roomsettings').run()
  assert.deepEqual(calls, [{ name: 'openRoomSettings', args: [c.room] }])
  assert.equal(pick(actionItems(registry, 'palette'), 'roomsettings').label, 'Room settings…')
  assert.equal(roomMenu(registry).some((a) => a.id === 'delete' || a.id === 'prsettings'), false)
  assert.equal(actionItems(registry, 'palette').some((a) => a.id === 'delete'), true)
})

test('/find opens unified search with its text; the palette has no separate find action', () => {
  const { context: c, calls } = context()
  const registry = actions(c)
  assert.equal(actionItems(registry, 'palette').some((a) => a.id === 'find'), false)
  pickName(actionItems(registry, 'slash'), 'find').run('deploy failed')
  assert.deepEqual(calls, [{ name: 'openSearch', args: ['deploy failed'] }])
})

test('there is no room Changes command', () => {
  const { context: c } = context({ id: 'room_test', name: 'test', repo: '/repo' })
  c.hasTerminal = true
  for (const surface of ['palette', 'slash'] as const) assert.equal(actionItems(actions(c), surface).some((a) => a.id === 'changes'), false)
})

test('availability matches across palette and slash input', () => {
  const { context: c } = context({ id: 'room_test', name: 'test', repo: '' })
  c.hasTerminal = false
  const registry = actions(c)
  for (const surface of ['palette', 'slash'] as const) {
    const ids = actionItems(registry, surface).map((a) => a.id)
    for (const id of ['files', 'github', 'issues', 'terminal']) assert.equal(ids.includes(id), false)
    for (const id of ['run', 'workflows']) assert.equal(ids.includes(id), true)
    assert.equal(ids.includes('jobs'), false)
  }
  c.room = null
  assert.equal(actionItems(actions(c), 'slash').length, 0)
})

test('room toggles preserve IDs, icons, order, and shared pin calculation', async () => {
  const { context: c, calls, room } = context()
  const sent: unknown[] = []
  const names: Record<string, string> = { PinRoom: 'pin', UnpinRoom: 'unpin', ArchiveRoom: 'archive', UnarchiveRoom: 'unarchive' }
  globalThis.fetch = async (path, options) => {
    sent.push([names[String(path).slice('/api/'.length)], ...JSON.parse(String(options?.body))])
    return Response.json(null)
  }
  c.rooms.push({ pinOrder: 4 })
  for (const pinned of [false, true]) {
    room.pinned = pinned
    room.archived = pinned
    const registry = actions(c)
    const menu = roomMenu(registry)
    assert.deepEqual(menu.map((a) => a.id), ['rename', 'room-pin', 'room-archive', 'roomsettings'])
    const palette = actionItems(registry, 'palette'), slash = actionItems(registry, 'slash')
    assert.equal(pick(palette, 'room-archive').icon, 'archive-icon')
    assert.equal(pick(slash, 'room-pin').name, pinned ? 'unpin' : 'pin')
    await pick(menu, 'room-pin').run()
    await pick(menu, 'room-archive').run()
  }
  assert.deepEqual(sent, [['pin', 'room_test', 5], ['archive', 'room_test'], ['unpin', 'room_test'], ['unarchive', 'room_test']])
  assert.equal(calls[0], 'test is pinned to the rail.')
})

test('the issues action opens the Issues tab and the prs action opens Pull requests', () => {
  const { context: c, calls } = context()
  const slash = actionItems(actions(c), 'slash')
  pickName(slash, 'issues').run()
  pickName(slash, 'prs').run()
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
