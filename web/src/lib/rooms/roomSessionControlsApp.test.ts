import test from 'node:test'
import assert from 'node:assert/strict'
import { JSDOM } from 'jsdom'
import { createRoomControls } from '../app/roomControls.svelte.ts'
import type { RoomControlValue } from './roomSessionControls.ts'
import { need } from '../../../test/need.ts'

Object.assign(globalThis, { $state: <T>(value: T) => value })

function app() {
  let room = 'a', request = 1, alive = true
  const visits: { changed: (value: RoomControlValue) => void; destroyed: boolean }[] = []
  const calls: string[] = []
  const api = { roomSessionControls: async () => ({}), pauseRoomSessions: async () => ({}), resumeRoomSessions: async () => ({}), clearDormantSessions: async () => ({}) }
  const controls = createRoomControls({
    api, room: () => room, request: () => request, alive: () => alive, visible: () => true,
    hierarchy: () => [], refresh() {}, error() {}, details() {},
    create: (_api, id, changed) => {
      const visit = { changed, destroyed: false }; visits.push(visit)
      return { load: async () => {}, destroy: () => { visit.destroyed = true }, run: async (action) => { calls.push(id + ':' + action) } }
    },
  })
  return { controls, visits, calls, switchRoom(id: string) { room = id; request++; controls.visit(id) }, stop() { alive = false; controls.destroy() } }
}
const value = (dormant: number, busy = false): RoomControlValue => ({
  state: { roomId: 'a', paused: false, running: 0, held: 0, dormant }, busy, loading: false,
  action: '', results: [], error: '', statusError: '',
})

const readValue = (controls: ReturnType<typeof createRoomControls>) => controls.value
const readConfirm = (controls: ReturnType<typeof createRoomControls>) => controls.confirm

test('A → B → A rejects old status and action callbacks, including teardown', () => {
  const { controls, visits, calls, switchRoom, stop } = app()
  controls.visit('a'); controls.openMenu(0, 0); const old = need(controls.menu)
  switchRoom('b'); switchRoom('a')
  need(visits[0]).changed(value(3)); assert.ok(controls.value === null); assert.equal(visits[0]?.destroyed, true)
  controls.items(old)[2]?.run(); assert.ok(controls.confirm === null)
  need(visits[2]).changed(value(3)); controls.askClear(); const current = need(readConfirm(controls))
  controls.clear(old); assert.deepEqual(calls, [])
  controls.clear(current); assert.deepEqual(calls, ['a:clearDormant'])
  stop(); need(visits[2]).changed(value(1)); assert.equal(readValue(controls)?.state?.dormant, 3)
})
test('clear is disabled for no candidates and resume remains available for recovery', () => {
  const { controls, visits } = app(); controls.visit('a'); controls.openMenu(0, 0)
  const context = need(controls.menu)
  need(visits[0]).changed(value(0))
  assert.equal(controls.items(context)[2]?.disabled, true)
  assert.equal(controls.items(context)[1]?.disabled, false)
  need(visits[0]).changed(value(3, true)); assert.ok(controls.items(context).every((item) => item.disabled))
})
test('closing header menu restores focus safely after reactive state is cleared', () => {
  const { controls } = app(); let focused = 0
  const anchor = new JSDOM().window.document.createElement('button')
  anchor.focus = () => { focused++; assert.equal(controls.menu, null) }
  controls.openMenu(0, 0, anchor); controls.closeMenu(); assert.equal(focused, 1)
})
test('sidebar clear opens a confirmation before status loads and confirms only once', () => {
  const { controls, calls } = app(); controls.visit('a'); controls.askClear(2)
  const context = need(controls.confirm); assert.equal(context.count, 2)
  controls.clear(context); controls.clear(context); assert.deepEqual(calls, ['a:clearDormant'])
})
