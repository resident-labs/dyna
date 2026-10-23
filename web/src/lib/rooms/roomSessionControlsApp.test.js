import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { parse } from 'svelte/compiler'
import vm from 'node:vm'
import { stripTypes } from '../../../test/svelte-source.ts'
const source = readFileSync(new URL('../../App.svelte', import.meta.url), 'utf8')
const code = parse(source).instance.content.body.filter((n) => n.type === 'FunctionDeclaration' && ['visitRoomControls', 'roomSessionActions', 'currentRoomControls', 'closeRoomSessionMenu', 'askClearDormant', 'roomSessionItems', 'clearRoomDormant'].includes(n.id.name)).map((n) => source.slice(n.start, n.end)).join('\n')
function app() {
  const visits = [], calls = []
  const c = { api: {}, roomId: 'a', roomRequest: 1, roomControlsVisit: 0, consolidationAlive: true, roomSessionController: null, loadMembers() {}, loadChanges() {}, createRoomSessionControls(api, room, changed) { const controller = { load() {}, destroy() { this.destroyed = true }, run(action) { calls.push([room, action]) } }; visits.push({ room, changed, controller }); return controller } }
  vm.createContext(c); vm.runInContext(stripTypes(code), c); return { c, visits, calls }
}
test('A → B → A rejects old status and action callbacks, including teardown', () => {
  const { c, visits, calls } = app(); c.visitRoomControls('a'); const context = { roomId: 'a', visit: c.roomControlsVisit }
  c.roomId = 'b'; c.roomRequest++; c.visitRoomControls('b'); c.roomId = 'a'; c.roomRequest++; c.visitRoomControls('a')
  visits[0].changed({ state: { paused: true } }); assert.equal(c.roomControls, null); assert.equal(visits[0].controller.destroyed, true)
  assert.equal(c.currentRoomControls(context), false)
  visits[2].changed({ state: { paused: false, dormant: 3 }, busy: false }); const current = { roomId: 'a', visit: c.roomControlsVisit }
  const items = c.roomSessionItems(current); items[2].run(); assert.equal(c.clearDormantConfirm.count, 3); assert.deepEqual(calls, [])
  c.clearRoomDormant(context); assert.deepEqual(calls, [])
  c.clearRoomDormant(current); assert.deepEqual(calls, [['a', 'clearDormant']]); assert.equal(c.clearDormantConfirm, null)
  c.consolidationAlive = false; visits[2].changed({ state: { paused: true } }); assert.equal(c.roomControls.state.paused, false)
})
test('clear is disabled for no candidates and resume remains available for recovery', () => {
  const { c } = app(); c.visitRoomControls('a'); c.roomControls = { state: { paused: false, dormant: 0 }, busy: false }
  const items = c.roomSessionItems({ roomId: 'a', visit: c.roomControlsVisit })
  assert.equal(items.length, 3); assert.equal(items[2].disabled, true); assert.equal(items[1].disabled, false)
  c.roomControls.busy = true; assert.ok(c.roomSessionItems({}).every((i) => i.disabled))
})
test('closing header menu restores focus safely after reactive state is cleared', () => {
  const { c } = app(); let focused = false; c.roomSessionActions(1, 2, { focus() { assert.equal(c.roomControlsMenu, null); focused = true } })
  c.closeRoomSessionMenu(); assert.equal(focused, true)
})

test('sidebar clear opens a confirmation before status loads and confirms only once', () => {
  const { c, calls } = app(); c.visitRoomControls('a')
  c.askClearDormant(3)
  assert.equal(c.clearDormantConfirm.count, 3)
  assert.deepEqual(calls, [])
  const context = c.clearDormantConfirm
  c.clearRoomDormant(context); c.clearRoomDormant(context)
  assert.deepEqual(calls, [['a', 'clearDormant']])
})
