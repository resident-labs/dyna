import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { parse } from 'svelte/compiler'
import vm from 'node:vm'
import { changesVisuals } from '../files/visuals.ts'
import { createReloadGate, listVisible } from '../ui/reloadGate.ts'
import { hasItemChange } from '../ui/listChanges.ts'
import { createRoomData } from './roomData.ts'
import { createRoomSessionControls } from './roomSessionControls.ts'
import { installRouterShim } from '../../../test/appRouterShim.ts'
import { reloadsUnread } from '../ui/unreadReload.ts'
import { sourceRange, stripTypes } from '../../../test/svelte-source.ts'
import { installSelection, installSessionView } from '../../../test/appModules.ts'

const source = readFileSync(new URL('../../App.svelte', import.meta.url), 'utf8')
const functions = parse(source, { modern: true }).instance?.content.body ?? []

interface ItemChange {
  op: string
  id?: string
  controls?: boolean
  consolidations?: boolean
}

interface AppEvent {
  kind: string
  roomId?: string
  change?: ItemChange
  posts?: { id: string; from?: string; to?: string[]; text?: string }[]
}

interface ControlsState {
  roomId?: string
  paused?: boolean
  running?: number
  held?: number
  dormant?: number
}

interface ControlsValue {
  state: ControlsState | null
  loading?: boolean
  busy?: boolean
  action?: string
  results?: unknown[]
  error?: string
  statusError?: string
}

interface SessionEventsState {
  view: string
  roomId: string
  roomRequest: number
  drill: { id?: string } | null
  home: null
  feed: null
  memberChips: boolean
  consolidationAlive: boolean
  mergeVersion: number
  reviewVersion: number
  openRun: string
  showRoomSettings?: boolean
  sidebarChips?: boolean
  sideVisible?: boolean
  controlsOpen?: boolean
  reviewOpen?: boolean
  roomControls: ControlsValue | null
  loadQuestions: () => void
  loadVisuals: () => void
  loadRooms: () => void
  loadReviewer: () => void
  loadChanges: () => void
  loadGoals: () => void
  loadIssues: () => void
  loadRules: () => void
  refreshHeldComments: () => void
  reviewerSoon: () => void
  soonRooms: (kind?: string) => unknown
  sessionData: { load: () => Promise<unknown> }
  workflowData?: { loadJobs: () => void }
  hasItemChange: typeof hasItemChange
  reloadsUnread: typeof reloadsUnread
  changesVisuals: typeof changesVisuals
  api: {
    members: () => Promise<unknown[]>
    sessionConsolidations: () => Promise<unknown[]>
    roomSessionControls: (room: string) => Promise<Required<ControlsState>>
    pauseRoomSessions: (room: string) => Promise<unknown>
    resumeRoomSessions: (room: string) => Promise<unknown>
    clearDormantSessions: (room: string) => Promise<unknown>
  }
}

interface SessionEventsApp extends SessionEventsState {
  reload: ReturnType<typeof createReloadGate>
  roomData: ReturnType<typeof createRoomData>
  roomSessionController: ReturnType<typeof createRoomSessionControls>
  handle: (event: AppEvent) => void
  resync: () => Promise<void>
}

function app(view: string) {
  const calls: string[] = []
  const state: SessionEventsState = {
    hasItemChange, reloadsUnread, view, mergeVersion: 0, roomId: 'a', roomRequest: 1, drill: null, home: null, feed: null,
    memberChips: true, consolidationAlive: true, openRun: '',
    roomControls: null, loadQuestions() {}, loadVisuals() {}, loadRooms() {}, loadReviewer() {}, loadChanges() {}, loadGoals() {}, loadIssues() {}, loadRules() {}, refreshHeldComments() {}, reviewerSoon() {},
    soonRooms: (kind = 'rooms') => kind === 'controls' ? app.roomSessionController.load() : calls.push(kind),
    sessionData: { load: async () => calls.push('transcript') },
    reviewVersion: 0,
    changesVisuals,
    api: {
      members: async () => { calls.push('members'); return [] },
      sessionConsolidations: async () => { calls.push('consolidations'); return [] },
      roomSessionControls: async () => { calls.push('controls'); return { roomId: 'a', paused: false, running: 0, held: 0, dormant: 0 } },
      // These tests never run a pause, resume or clear action.
      pauseRoomSessions: async () => ({}),
      resumeRoomSessions: async () => ({}),
      clearDormantSessions: async () => ({}),
    },
  }
  const reload = createReloadGate(() => ({ room: state.roomId, visit: state.roomRequest }), (name) => listVisible(name, state))
  const roomData = createRoomData(state.api, { current: () => ({ room: state.roomId, visit: state.roomRequest }), members() {}, choices() {} })
  const roomSessionController = createRoomSessionControls(state.api, 'a', (value: ControlsValue) => { state.roomControls = value }, () => listVisible('roomControls', state))
  const app: SessionEventsApp = Object.assign(state, { reload, roomData, roomSessionController }) as SessionEventsApp
  // The pull request and question views live in lib/app modules; the sliced
  // handlers read them, so they answer from this state.
  Reflect.set(app, 'changeView', { bumpVersion: () => { app.mergeVersion += 1 } })
  Reflect.set(app, 'questionView', { setQuestions: () => {} })
  Reflect.set(app, 'pinView', { setPins: () => {}, clear: () => {} })
  Reflect.set(app, 'issueView', { clear: () => {} })
  // The transcript lives in lib/app/sessionView.svelte.ts now; its controller
  // is the test's own, so the counts stay visible.
  installSessionView(app, state.sessionData)
  // The screen state lives in the router now, so the sliced functions write through it.
  installRouterShim(app)
  vm.createContext(app)
  for (const name of ['applyItemEvent', 'handle', 'loadMembers', 'resync']) {
    const node = functions.find((n) => n.type === 'FunctionDeclaration' && n.id?.name === name)
    assert.ok(node, name)
    const { start, end } = sourceRange(node)
    vm.runInContext(stripTypes(source.slice(start, end)), app)
  }
  const fns = vm.runInContext('({ applyItemEvent, handle, loadSession, loadMembers, resync })', app) as Pick<SessionEventsApp, 'handle' | 'resync'>
  Object.assign(app, fns)
  return { c: app, calls, close() { app.roomData.destroy(); app.roomSessionController.destroy() } }
}

for (const view of ['github', 'channel:general']) {
  test(`${view}: streaming output skips room lists and only reloads the selected session`, async () => {
    const { c, calls, close } = app(view)
    c.handle({ kind: 'session', roomId: 'a', change: { op: 'upsert', id: 'other' } })
    await new Promise(setImmediate)
    assert.deepEqual(calls, [])
    c.drill = { id: 'one' }
    c.handle({ kind: 'session', roomId: 'a', change: { op: 'upsert', id: 'other' } })
    c.handle({ kind: 'session', roomId: 'b', change: { op: 'upsert', id: 'one' } })
    assert.deepEqual(calls, [])
    c.handle({ kind: 'session', roomId: 'a', change: { op: 'upsert', id: 'one' } })
    await new Promise(setImmediate)
    // The review workspace can refresh without a second status-count loader.
    assert.deepEqual(calls.sort(), ['transcript'])
    assert.equal(c.reviewVersion, 1)
    close()
  })
}

test('member metadata refreshes controls only when their flags change', async () => {
  const { c, calls, close } = app('github')
  c.handle({ kind: 'members', roomId: 'a', change: { op: 'upsert', id: 'one' } })
  await new Promise(setImmediate)
  assert.deepEqual(calls, ['feed', 'members', 'rooms'])
  calls.length = 0
  c.handle({ kind: 'members', roomId: 'a', change: { op: 'upsert', id: 'one', controls: true, consolidations: true } })
  await new Promise(setImmediate)
  assert.deepEqual(calls, ['feed', 'members', 'controls', 'rooms'])
  close()
})

test('reconnect refreshes visible controls without consolidation history', async () => {
  const { c, calls, close } = app('github')
  c.loadRooms = () => calls.push('rooms')
  c.roomData.open = async () => true
  c.openRun = ''
  for (const name of ['loadChanges', 'loadGoals', 'loadRules', 'loadVisuals'] as const) c[name] = () => {}
  let reviewerLoads = 0
  c.loadReviewer = () => reviewerLoads++
  const node = functions.find((n) => n.type === 'FunctionDeclaration' && n.id?.name === 'resync')
  assert.ok(node, 'resync')
  const { start, end } = sourceRange(node)
  vm.runInContext(stripTypes(source.slice(start, end)), c)
  await c.resync()
  await new Promise(setImmediate)
  assert.deepEqual(calls, ['rooms', 'controls'])
  // Reviewer settings that changed while the stream was down show again.
  assert.equal(reviewerLoads, 1)
  assert.equal(c.mergeVersion, 1)
  close()
})

test('reconnect refreshes the open session and its review workspace', async () => {
  const { c, calls, close } = app('github')
  c.loadRooms = () => {}
  c.roomData.open = async () => true
  c.openRun = ''
  for (const name of ['loadChanges', 'loadGoals', 'loadRules', 'loadVisuals', 'loadReviewer'] as const) c[name] = () => {}
  c.drill = { id: 'one' }
  const node = functions.find((n) => n.type === 'FunctionDeclaration' && n.id?.name === 'resync')
  assert.ok(node, 'resync')
  const { start, end } = sourceRange(node)
  vm.runInContext(stripTypes(source.slice(start, end)), c)
  await c.resync()
  await new Promise(setImmediate)
  assert.equal(c.reviewVersion, 1)
  assert.ok(calls.includes('transcript'), `calls: ${calls}`)
  close()
})

test('the remaining timer refreshes bulk transfers only while the page is visible', () => {
  const calls: string[] = []
  const c = { needPair: false, pageVisible: true, bulkControllers: new Map([['one', { refreshTransfers: () => calls.push('transfer') }]]), changesVisuals }
  // The transfer poll asks the selection module, which owns the batch
  // controllers; the state above holds them here.
  installSelection(c)
  installRouterShim(c)
  vm.createContext(c)
  const mounted = functions.find((n): n is Extract<typeof n, { type: 'ExpressionStatement' }> => n.type === 'ExpressionStatement' && n.expression.type === 'CallExpression' && n.expression.callee.type === 'Identifier' && n.expression.callee.name === 'onMount')
  assert.ok(mounted, 'App mounts onMount')
  const mountCall = mounted.expression
  assert.ok(mountCall.type === 'CallExpression')
  const callback = mountCall.arguments[0]
  assert.ok(callback && callback.type === 'ArrowFunctionExpression' && callback.body.type === 'BlockStatement', 'onMount takes a callback')
  const binding = callback.body.body.find((n) => n.type === 'VariableDeclaration' && n.declarations.some((d) => d.id.type === 'Identifier' && d.id.name === 'transferPoll'))
  assert.ok(binding && binding.type === 'VariableDeclaration', 'App declares transferPoll')
  const declaration = binding.declarations.find((d) => d.id.type === 'Identifier' && d.id.name === 'transferPoll')
  assert.ok(declaration?.init, 'transferPoll is initialised')
  assert.ok(declaration.init.type === 'CallExpression', 'transferPoll starts a timer')
  const polling = declaration.init.arguments[0]
  assert.ok(polling, 'the timer takes a callback')
  const { start, end } = sourceRange(polling)
  const poll = vm.runInContext(stripTypes(source.slice(start, end)), c) as () => void
  poll()
  assert.deepEqual(calls, ['transfer'])
  calls.length = 0
  c.pageVisible = false
  poll()
  assert.deepEqual(calls, [])
})

test('room summary changes skip controls, while explicit pause changes refresh them', async () => {
  const { c, calls, close } = app('github')
  c.handle({ kind: 'rooms' })
  await new Promise(setImmediate)
  assert.deepEqual(calls, ['rooms'])
  calls.length = 0
  c.handle({ kind: 'room.paused', roomId: 'a' })
  await new Promise(setImmediate)
  assert.deepEqual(calls, ['controls', 'rooms'])
  close()
})

for (const scenario of [
  { name: 'paused visible room', paused: true, visible: true, room: 'a', expected: 1 },
  { name: 'unpaused room', paused: false, visible: true, room: 'a', expected: 0 },
  { name: 'hidden controls', paused: true, visible: false, room: 'a', expected: 0 },
  { name: 'another room', paused: true, visible: true, room: 'other', expected: 0 },
]) test(`${scenario.name}: pending posts refresh held counts only when relevant`, async () => {
  const { c, calls, close } = app('github')
  let held = 0
  c.api.roomSessionControls = async () => {
    calls.push('controls')
    return { roomId: 'a', paused: scenario.paused, running: 0, held, dormant: 0 }
  }
  await c.roomSessionController.load()
  assert.equal(c.roomControls?.state?.held, 0)
  calls.length = 0
  c.memberChips = scenario.visible
  held = 1
  c.handle({ kind: 'posts', roomId: scenario.room, posts: [{ id: 'new', from: 'a', to: ['a:one'], text: 'pending work' }] })
  await new Promise(setImmediate)
  assert.equal(calls.filter((name) => name === 'controls').length, scenario.expected)
  assert.equal(c.roomControls?.state?.held, scenario.expected)
  close()
})

for (const cached of [false, true]) test(`a post during ${cached ? 'an unpaused-state' : 'the initial'} control load queues a trailing held-count refresh`, async () => {
  const { c, close } = app('github')
  if (cached) await c.roomSessionController.load()
  let finish: (value: Required<ControlsState>) => void = () => { throw new Error('no pending control load') }
  let calls = 0
  const first = new Promise<Required<ControlsState>>((resolve) => { finish = resolve })
  c.api.roomSessionControls = () => ++calls === 1 ? first : Promise.resolve({ roomId: 'a', paused: true, running: 0, held: 1, dormant: 0 })
  const pending = c.roomSessionController.load()
  c.handle({ kind: 'posts', roomId: 'a', posts: [{ id: 'new', to: ['a:one'] }] })
  finish({ roomId: 'a', paused: true, running: 0, held: 0, dormant: 0 })
  await pending
  assert.equal(calls, 2)
  assert.equal(c.roomControls?.state?.held, 1)
  close()
})

test('merge refresh version follows room PR and change events only', () => {
  const { c, close } = app('github')
  c.refreshHeldComments = () => {}
  c.reviewerSoon = () => {}
  c.loadChanges = () => {}
  c.handle({ kind: 'prs', roomId: 'b' })
  assert.equal(c.mergeVersion, 0)
  c.handle({ kind: 'prs', roomId: 'a' })
  c.handle({ kind: 'changes', roomId: 'a' })
  assert.equal(c.mergeVersion, 2)
  close()
})

test('a jobs metadata event reloads jobs only for the open room', () => {
  const { c, calls, close } = app('jobs')
  c.workflowData = { loadJobs: () => calls.push('jobs') }
  c.handle({ kind: 'jobs', roomId: 'other' })
  assert.deepEqual(calls, [])
  c.handle({ kind: 'jobs', roomId: 'a' })
  assert.deepEqual(calls, ['members', 'jobs'])
  close()
})
