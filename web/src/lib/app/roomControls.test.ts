import test from 'node:test'
import assert from 'node:assert/strict'
import { need } from '../../../test/need.ts'
import { createRoomControls } from './roomControls.svelte.ts'
import type { RoomControlValue } from '../rooms/roomSessionControls.ts'

test('ignores callbacks and clear actions from an earlier room visit', () => {
  let room = 'a', request = 1
  const callbacks: ((value: RoomControlValue) => void)[] = []
  let runs = 0
  const run = async () => { runs++ }
  Object.assign(globalThis, { $state: <T>(value: T) => value })
  const api = {
    roomSessionControls: async () => ({}), pauseRoomSessions: async () => ({}),
    resumeRoomSessions: async () => ({}), clearDormantSessions: async () => ({}),
  }
  const controls = createRoomControls({
    api, room: () => room, request: () => request, alive: () => true, visible: () => true,
    hierarchy: () => [], refresh: () => {}, error: () => {}, details: () => {},
    create: (_api, _room, changed) => {
      callbacks.push(changed)
      return { load: async () => {}, run, destroy: () => {} }
    },
  })
  const value: RoomControlValue = {
    state: { roomId: 'a', paused: false, running: 0, held: 0, dormant: 2 },
    loading: false, busy: false, action: '', results: [], error: '', statusError: '',
  }
  controls.visit('a')
  need(callbacks[0])(value)
  controls.askClear()
  const old = controls.confirm
  assert.ok(old)
  room = 'b'; request++
  controls.visit('b')
  need(callbacks[0])(value)
  if (old) controls.clear(old)
  assert.equal(controls.value, null)
  assert.equal(controls.confirm, null)
  assert.equal(runs, 0)
})
