import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { parse } from 'svelte/compiler'
import vm from 'node:vm'
import { sourceRange, stripTypes } from '../../../test/svelte-source.ts'
const source = readFileSync(new URL('../../App.svelte', import.meta.url), 'utf8')
const code = (parse(source, { modern: true }).instance?.content.body ?? []).filter((n) => n.type === 'FunctionDeclaration' && ['visitRoomControls', 'roomSessionActions', 'currentRoomControls', 'closeRoomSessionMenu', 'askClearDormant', 'roomSessionItems', 'clearRoomDormant'].includes(n.id?.name ?? '')).map((n) => {
  const { start, end } = sourceRange(n)
  return source.slice(start, end)
}).join('\n')

interface ControlsState {
  paused?: boolean
  dormant?: number
}

interface ControlsValue {
  state: ControlsState
  busy?: boolean
}

interface Controller {
  load(): void
  destroy(): void
  run(action: string): void
  destroyed?: boolean
}

interface Visit {
  room: string
  changed: (value: ControlsValue) => void
  controller: Controller
}

interface MenuItem {
  label?: string
  disabled?: boolean
  run(): void
}

interface Context {
  roomId?: string
  visit?: number
}

interface ClearConfirm extends Context {
  count: number
}

interface ControlsAppState {
  api: Record<string, unknown>
  roomId: string
  roomRequest: number
  roomControlsVisit: number
  consolidationAlive: boolean
  roomSessionController: Controller | null
  roomControls: ControlsValue | null
  clearDormantConfirm: ClearConfirm | null
  roomControlsMenu: unknown
  loadMembers(): void
  loadChanges(): void
  createRoomSessionControls(api: unknown, room: string, changed: (value: ControlsValue) => void): Controller
}

interface ControlsApp extends ControlsAppState {
  visitRoomControls(room: string): void
  currentRoomControls(context: Context): boolean
  roomSessionItems(context: Context): [MenuItem, MenuItem, MenuItem]
  clearRoomDormant(context: Context): void
  askClearDormant(count: number): void
  roomSessionActions(visit: number, other: number, element: { focus(): void }): void
  closeRoomSessionMenu(): void
}

// Reads the saved controls without the narrowing that an assertion leaves behind.
const controls = (app: ControlsApp): ControlsValue | null => app.roomControls

function app() {
  const visits: Visit[] = [], calls: [string, string][] = []
  const state: ControlsAppState = {
    api: {}, roomId: 'a', roomRequest: 1, roomControlsVisit: 0, consolidationAlive: true, roomSessionController: null, roomControls: null, clearDormantConfirm: null, roomControlsMenu: null,
    loadMembers() {}, loadChanges() {},
    createRoomSessionControls(_api: unknown, room: string, changed: (value: ControlsValue) => void) {
      const controller: Controller = { load() {}, destroy() { this.destroyed = true }, run(action: string) { calls.push([room, action]) } }
      visits.push({ room, changed, controller }); return controller
    },
  }
  vm.createContext(state); vm.runInContext(stripTypes(code), state)
  const fns = vm.runInContext('({ visitRoomControls, roomSessionActions, currentRoomControls, closeRoomSessionMenu, askClearDormant, roomSessionItems, clearRoomDormant })', state) as Pick<ControlsApp, 'visitRoomControls' | 'roomSessionActions' | 'currentRoomControls' | 'closeRoomSessionMenu' | 'askClearDormant' | 'roomSessionItems' | 'clearRoomDormant'>
  return { c: Object.assign(state, fns) as ControlsApp, visits, calls }
}

test('A → B → A rejects old status and action callbacks, including teardown', () => {
  const { c, visits, calls } = app(); c.visitRoomControls('a'); const context = { roomId: 'a', visit: c.roomControlsVisit }
  c.roomId = 'b'; c.roomRequest++; c.visitRoomControls('b'); c.roomId = 'a'; c.roomRequest++; c.visitRoomControls('a')
  visits[0]?.changed({ state: { paused: true } }); assert.equal(controls(c), null); assert.equal(visits[0]?.controller.destroyed, true)
  assert.equal(c.currentRoomControls(context), false)
  visits[2]?.changed({ state: { paused: false, dormant: 3 }, busy: false }); const current = { roomId: 'a', visit: c.roomControlsVisit }
  const items = c.roomSessionItems(current); items[2].run(); assert.equal(c.clearDormantConfirm?.count, 3); assert.deepEqual(calls, [])
  c.clearRoomDormant(context); assert.deepEqual(calls, [])
  c.clearRoomDormant(current); assert.deepEqual(calls, [['a', 'clearDormant']]); assert.equal(c.clearDormantConfirm, null)
  c.consolidationAlive = false; visits[2]?.changed({ state: { paused: true } }); assert.equal(controls(c)?.state.paused, false)
})
test('clear is disabled for no candidates and resume remains available for recovery', () => {
  const { c } = app(); c.visitRoomControls('a'); c.roomControls = { state: { paused: false, dormant: 0 }, busy: false }
  const items = c.roomSessionItems({ roomId: 'a', visit: c.roomControlsVisit })
  assert.equal(items.length, 3); assert.equal(items[2].disabled, true); assert.equal(items[1].disabled, false)
  assert.ok(c.roomControls)
  c.roomControls.busy = true; assert.ok(c.roomSessionItems({}).every((i) => i.disabled))
})
test('closing header menu restores focus safely after reactive state is cleared', () => {
  const { c } = app(); let focused = false; c.roomSessionActions(1, 2, { focus() { assert.equal(c.roomControlsMenu, null); focused = true } })
  c.closeRoomSessionMenu(); assert.equal(focused, true)
})

test('sidebar clear opens a confirmation before status loads and confirms only once', () => {
  const { c, calls } = app(); c.visitRoomControls('a')
  c.askClearDormant(3)
  assert.equal(c.clearDormantConfirm?.count, 3)
  assert.deepEqual(calls, [])
  const context = c.clearDormantConfirm
  assert.ok(context)
  c.clearRoomDormant(context); c.clearRoomDormant(context)
  assert.deepEqual(calls, [['a', 'clearDormant']])
})
