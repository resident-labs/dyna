import test from 'node:test'
import assert from 'node:assert/strict'
import { api } from '../../api.ts'
import { createPalette } from './palette.svelte.ts'
import { need } from '../../../test/need.ts'
import type { Room } from '../api/room.ts'

Object.assign(globalThis, { $state: <T>(value: T) => value })

test('a slash rename targets the current room while the palette stays closed', async (t) => {
  const room = (id: string): Room => ({ id, name: id, pinned: false, pinOrder: 0, archived: false, lastActivity: '', unread: 0, running: 0, members: 0 })
  let current = room('a')
  const calls: string[] = []
  t.mock.method(api, 'renameRoom', async (id: string) => { calls.push(id); return null })
  const palette = createPalette({
    room: () => current, roomId: () => current.id, rooms: () => [current], runs: () => null,
    members: () => [], changes: () => [], session: () => null, sessionPRs: () => null,
    narrow: () => false, hasTerminal: () => false,
    openView() {}, openSettings() {}, renameRoom() {}, deleteRoom() {}, roomAction() {}, say() {},
    loadRooms: async () => {}, selectRoom: async () => true, openFiles() {}, openPins() {},
    openRoomSettings() {}, openSessionPRs() {}, cancelRun() {}, toggleSidebar() {},
    startWorkflowDialog() {}, openSession() {}, openRun() {}, openPR() {},
  })
  const command = need(palette.composerCommands.find((item) => item.name === 'rename'))
  current = room('b')
  await command.run('new-name')
  assert.deepEqual(calls, ['b'])
  assert.equal(palette.show, false)
})
