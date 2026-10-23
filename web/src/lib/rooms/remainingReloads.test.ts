import { test } from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import { parse } from 'svelte/compiler'
import type { Program } from 'estree'
import { changesVisuals } from '../files/visuals.ts'
import { createRoomData } from './roomData.ts'
import { mergeSessionPage } from '../sessions/sessionPages.ts'
import { createSessionData } from '../sessions/sessionData.ts'
import { createRoomSessionControls } from './roomSessionControls.ts'
import { applyEvent, openedApps, pushAction, type Computer } from '../computers/computers.ts'
import { hasItemChange } from '../ui/listChanges.ts'
import { createReloadGate, listVisible, reconcileRows } from '../ui/reloadGate.ts'
import { reloadsUnread } from '../ui/unreadReload.ts'
import { inRoomFeed } from './feedViews.ts'
import { call, runFunctions, sourceRange, stripTypes } from '../../../test/svelte-source.ts'
import { installRouterShim } from '../../../test/appRouterShim.ts'
import { installRoomLoader } from '../../../test/appRoomLoader.ts'
import { ISSUES_FUNCTIONS, PINS_FUNCTIONS, PRS_FUNCTIONS, QUESTIONS_FUNCTIONS, SESSION_VIEW_FUNCTIONS, SYNC_FUNCTIONS, installIssues, installPins, installPRs, installQuestions, installSessionView, installSync } from '../../../test/appModules.ts'
import { need } from '../../../test/need.ts'
import { member } from '../../../test/session-fixtures.ts'
import type { Member } from '../api/room.ts'

// AppState is the bag of App.svelte state a test builds. appFunctions adds the
// component's own functions to it at run time, so every context type stays open.
type AppState = Record<string, unknown>

const deferred = <T = never>() => {
  let resolve: (value: T) => void = () => {}
  const promise = new Promise<T>((yes) => { resolve = yes })
  return { promise, resolve }
}

const source = readFileSync(new URL('../../App.svelte', import.meta.url), 'utf8')
const body: Program['body'] = parse(source, { modern: true }).instance?.content.body ?? []

// The parsed script body, and the expression and call nodes the helpers narrow to.
type ScriptStatement = Program['body'][number]
type ScriptExpression = Extract<ScriptStatement, { type: 'ExpressionStatement' }>['expression']
type CallNode = Extract<ScriptExpression, { type: 'CallExpression' }>

// isCallTo reports a call expression whose callee is the named identifier.
function isCallTo(node: ScriptExpression, name: string): node is CallNode {
  return node.type === 'CallExpression' && node.callee.type === 'Identifier' && node.callee.name === name
}

// blockOf is the statement list of a function expression, or an empty list.
function blockOf(node: ScriptExpression | undefined): ScriptStatement[] {
  if (!node || (node.type !== 'ArrowFunctionExpression' && node.type !== 'FunctionExpression')) return []
  return node.body.type === 'BlockStatement' ? node.body.body : []
}

// onMountBody is the statement list of App.svelte's onMount callback.
function onMountBody(): ScriptStatement[] {
  for (const node of body) {
    if (node.type !== 'ExpressionStatement' || !isCallTo(node.expression, 'onMount')) continue
    const callback = node.expression.arguments[0]
    return blockOf(callback?.type === 'SpreadElement' ? undefined : callback)
  }
  throw new Error('App.svelte has no onMount callback')
}

// initOf is the initializer of a declared variable in these statements.
function initOf(statements: ScriptStatement[], name: string): ScriptExpression | undefined {
  for (const node of statements) {
    if (node.type !== 'VariableDeclaration') continue
    for (const declarator of node.declarations) {
      if (declarator.id.type !== 'Identifier' || declarator.id.name !== name || !declarator.init) continue
      return declarator.init
    }
  }
  return undefined
}

// findStatement is the first call statement to name whose text includes marker.
function findStatement(callName: string, marker: string): ScriptStatement | undefined {
  for (const node of body) {
    if (node.type !== 'ExpressionStatement' || !isCallTo(node.expression, callName)) continue
    const { start, end } = sourceRange(node)
    if (source.slice(start, end).includes(marker)) return node
  }
  return undefined
}

// runExpression runs one node of App.svelte's source in the context.
function runExpression(c: AppState, node: object, wrap = false): unknown {
  const { start, end } = sourceRange(node)
  const code = source.slice(start, end)
  return vm.runInContext(stripTypes(wrap ? `(${code})` : code), c)
}

// sessionDataFor pairs the test's read doubles with the controller. These tests
// drive one read path, so the other reads and hooks are absent on purpose.
function sessionDataFor(api: unknown, hooks: unknown) {
  return createSessionData(api as Parameters<typeof createSessionData>[0], hooks as Parameters<typeof createSessionData>[1])
}

// The lists moved to lib/app modules; a test that names one of their loaders
// installs that module over the same state, as appRoomLoader.ts does the room data.
const MODULES: readonly [readonly string[], (state: object) => void][] = [
  [QUESTIONS_FUNCTIONS, installQuestions],
  [PINS_FUNCTIONS, installPins],
  [ISSUES_FUNCTIONS, installIssues],
  [SYNC_FUNCTIONS, installSync],
  [PRS_FUNCTIONS, installPRs],
  [SESSION_VIEW_FUNCTIONS, installSessionView],
]

function appFunctions(c: AppState, names: string[]): void {
  c.hasItemChange = hasItemChange
  c.reloadsUnread = reloadsUnread
  c.inRoomFeed = inRoomFeed
  if (names.includes('handle') && !names.includes('applyItemEvent')) names = ['applyItemEvent', ...names]
  c.changesVisuals = changesVisuals
  for (const [k, v] of Object.entries({ mergeVersion: 0, routedParents: "", feed: null, roomId: "" })) if (!(k in c)) c[k] = v
  c.refreshHeldComments ??= () => {}
  // The screen state lives in the router now, so the sliced functions write through it.
  installRouterShim(c)
  const moved = new Set<string>()
  for (const [functions, install] of MODULES) {
    if (!functions.some((name) => names.includes(name))) continue
    install(c)
    for (const name of functions) moved.add(name)
  }
  // The views a sliced App function reads stay simple unless its module is installed.
  c.changeView ??= { bumpVersion: () => { c.mergeVersion = Number(c.mergeVersion ?? 0) + 1 } }
  c.questionView ??= { setQuestions: (rows: unknown) => { c.questions = rows } }
  c.pinView ??= { setPins: (rows: unknown) => { c.pins = rows }, clear: () => { c.pins = [] } }
  c.issueView ??= { clear: () => { c.issues = []; c.issueHistory = [] } }
  c.syncView ??= {}
  c.goalView ??= { clear: () => { c.goals = [] }, setGoals: (rows: unknown) => { c.goals = rows }, trackFetchedGoal: () => {} }
  c.machineView ??= { clear: () => {}, changed: () => {} }
  runFunctions(source, names.filter((name) => !moved.has(name)), c)
}

test('room summary bursts share one request plus one trailing request', async () => {
  const waits = [deferred<unknown[]>(), deferred<unknown[]>()]
  let calls = 0
  const data = createRoomData({ rooms: () => waits[calls++]?.promise }, {
    current: () => ({ room: 'a', visit: 1 }), rooms() {}, error() {}, members() {}, choices() {},
  })
  const requests = Array.from({ length: 10 }, () => data.loadRooms())
  assert.equal(calls, 1)
  waits[0]?.resolve([])
  await new Promise(setImmediate)
  assert.equal(calls, 2)
  waits[1]?.resolve([])
  await Promise.all(requests)
  data.destroy()
})
test('member status refreshes do not fetch mention choices for a hidden composer', async () => {
  let mentions = 0
  const data = createRoomData({
    members: async () => [member('one')],
    mentions: async () => { mentions++; return ['@one'] },
  }, {
    current: () => ({ room: 'a', visit: 1 }), visible: (name: string) => name !== 'mentions',
    members() {}, choices() {},
  })
  await data.loadMembers()
  assert.equal(mentions, 0)
  data.destroy()
})
test('session controls skip hidden consumers and merge visible refresh bursts', async () => {
  let visible = false, calls = 0
  const waits = [deferred<unknown>(), deferred<unknown>()]
  const control = createRoomSessionControls({ roomSessionControls: () => need(waits[calls++], 'controls answer').promise, pauseRoomSessions: async () => ({}), resumeRoomSessions: async () => ({}), clearDormantSessions: async () => ({}) }, 'a', () => {}, () => visible)
  const hidden = control.load()
  assert.equal(calls, 0)
  await hidden
  visible = true
  const requests = Array.from({ length: 10 }, () => control.load())
  assert.equal(calls, 1)
  waits[0]?.resolve({ roomId: 'a', paused: false, running: 0, held: 0, dormant: 0 })
  await new Promise(setImmediate)
  assert.equal(calls, 2)
  waits[1]?.resolve({ roomId: 'a', paused: false, running: 0, held: 0, dormant: 0 })
  await Promise.all(requests)
  control.destroy()
})
test('computer lists load only for visible badges, session chips or desktops', () => {
  const c = { view: 'github', memberChips: false, sideVisible: false, drill: null }
  assert.equal(listVisible('computers', c), false)
  assert.equal(listVisible('computers', { ...c, sideVisible: true }), true)
  assert.equal(listVisible('computers', { ...c, memberChips: true }), true)
  assert.equal(listVisible('computers', { ...c, drill: { id: 'a' } }), true)
  assert.equal(listVisible('computers', { ...c, view: 'computers' }), true)
})
test('mention choices follow roster changes and wait until their consumer opens', async () => {
  let visible = false, calls = 0
  let members: Member[] = [member('one', { state: 'idle' })]
  const updates: unknown[] = []
  const data = createRoomData({
    members: async () => members,
    mentions: async () => { calls++; return ['@one'] },
  }, {
    current: () => ({ room: 'a', visit: 1 }),
    visible: (name: string) => name !== 'mentions' || visible,
    members() {}, choices: (value: unknown) => updates.push(value),
  })
  await data.loadMembers()
  assert.equal(calls, 0)
  visible = true
  await data.loadMentions()
  assert.equal(calls, 1)
  members = [{ ...need(members[0], 'first member'), state: 'live' }]
  await data.loadMembers()
  assert.equal(calls, 1)
  members = [...members, member('two')]
  await data.loadMembers()
  assert.equal(calls, 2)
  assert.equal(updates.length, 2)
  data.destroy()
})
test('room summaries drop hidden-page responses and queued reloads', async () => {
  let visible = true, calls = 0, updates = 0
  const wait = deferred<unknown[]>()
  const data = createRoomData({ rooms: () => { calls++; return wait.promise } }, {
    current: () => ({ room: 'a', visit: 1 }), visible: () => visible,
    rooms: () => { updates++ }, error() {}, members() {}, choices() {},
  })
  const old = data.loadRooms(); data.loadRooms()
  visible = false
  wait.resolve([])
  await old
  assert.equal(calls, 1)
  assert.equal(updates, 0)
  await data.loadRooms()
  assert.equal(calls, 1)
  data.destroy()
})
test('list-only events do not reload room summaries', () => {
  let summaries = 0, feeds = 0, members = 0
  const c: AppState = {
    roomId: 'a', home: null, feed: null, drill: null, roomSessionController: null, openRun: '',
    soonRooms: (kind = 'rooms') => kind === 'feed' ? feeds++ : kind === 'members' ? members++ : summaries++,
  }
  for (const name of ['loadQuestions', 'loadVisuals', 'loadGoals', 'loadRules', 'loadPins', 'reviewerSoon', 'loadChanges', 'loadMembers', 'loadConsolidations', 'loadRuns']) c[name] = () => {}
  appFunctions(c, ['handle'])
  for (const kind of ['prs', 'goals', 'questions', 'pins', 'runs']) call(c, 'handle', { kind, roomId: 'a' })
  assert.equal(summaries, 0)
  // The responsibility counts of the member rows change with these, so they
  // ask for the member kind instead of a room summary.
  assert.equal(members, 4, 'prs, goals, questions and runs reload the member rows')
  for (const kind of ['posts', 'members', 'rooms', 'room.paused', 'room.resumed']) call(c, 'handle', { kind, roomId: 'a' })
  assert.equal(summaries, 5)
  assert.equal(feeds, 2, 'posts and members refresh channel counts separately')
})
test('a room snapshot supersedes an older mention request for the same visit', async () => {
  const old = deferred<string[]>(), updates: unknown[] = []
  const data = createRoomData({
    members: async () => [member('old')],
    mentions: () => old.promise,
    openRoom: async () => ({ members: [member('new')], choices: ['@new'] }),
  }, {
    current: () => ({ room: 'a', visit: 1 }), visible: () => true,
    members() {}, choices: (value: unknown) => updates.push(value), opened: (value: { choices: unknown }) => updates.push(value.choices),
  })
  const pending = data.loadMembers()
  await new Promise(setImmediate)
  await data.open()
  old.resolve(['@old'])
  await pending
  assert.deepEqual(updates.at(-1), ['@new'])
  data.destroy()
})

// RestoreApp is the state the visibility restore callback reads.
interface RestoreApp extends AppState {
  roomId: string
  roomRequest: number
  pageVisible: boolean
  started: boolean
  view: string
  drill: null
  document: { visibilityState: string }
  questionsRequest: number
  questions: { id: string }[]
  pins: { id: string }[]
  syncStatus: Record<string, { origin: { ahead: number; behind: number } }>
  issues: { id: string }[]
  issueHistory: unknown[]
  showIssues: boolean
  loadError: string
  members: Member[]
  choices: string[]
  reconcileRows: typeof reconcileRows
  reload?: ReturnType<typeof createReloadGate>
  roomData?: ReturnType<typeof createRoomData>
  api: {
    rooms(): Promise<{ id: string }[]>
    members(): Promise<Member[]>
    mentions(): Promise<string[]>
    questions(): Promise<Record<string, { id: string }[]>>
    pins(): Promise<{ id: string }[]>
    syncStatus(): Promise<Record<string, { origin: { ahead: number; behind: number } }>>
    issues(): Promise<{ id: string }[]>
  }
}

test('visibility restoration refreshes skipped members, choices, questions and pins without another event', async () => {
  const calls: string[] = []
  const c: RestoreApp = {
    roomId: 'a', roomRequest: 1, pageVisible: false, started: true,
    view: 'channel:general', drill: null, document: { visibilityState: 'hidden' },
    questionsRequest: 0, questions: [], pins: [], syncStatus: {}, issues: [], issueHistory: [], showIssues: false, loadError: '', reconcileRows,
    members: [member('old')], choices: ['@old'],
    api: {
      rooms: async () => { calls.push('rooms'); return [{ id: 'a' }] },
      members: async () => { calls.push('members'); return [member('new')] },
      mentions: async () => { calls.push('mentions'); return ['@new'] },
      questions: async () => { calls.push('questions'); return { a: [{ id: 'q-new' }] } },
      pins: async () => { calls.push('pins'); return [{ id: 'p-new' }] },
      syncStatus: async () => { calls.push('sync'); return { '/r': { origin: { ahead: 0, behind: 2 } } } },
      issues: async () => { calls.push('issues'); return [{ id: 'i-new' }] },
    },
  }
  const visible = (name: string): boolean => c.pageVisible && listVisible(name, c)
  c.reload = createReloadGate(() => ({ room: c.roomId, visit: c.roomRequest }), visible)
  c.roomData = createRoomData(c.api, {
    current: () => ({ room: c.roomId, visit: c.roomRequest }), visible,
    members: (value: Member[]) => { c.members = value },
    choices: (value: string[]) => { c.choices = value }, rooms() {}, error() {},
  })
  appFunctions(c, ['loadRooms', 'loadMembers', 'loadQuestions', 'loadPins', 'loadSync', 'loadIssues'])
  if (body.some((n) => n.type === 'FunctionDeclaration' && n.id.name === 'refreshVisibleRoom')) appFunctions(c, ['refreshVisibleRoom'])
  const visibility = initOf(onMountBody(), 'visibility')
  if (!visibility) throw new Error('App.svelte has no visibility callback')
  c.restoreVisibility = runExpression(c, visibility)
  await Promise.all([call(c, 'loadMembers'), call(c, 'loadQuestions'), call(c, 'loadPins'), call(c, 'loadSync'), call(c, 'loadIssues')])
  assert.deepEqual(calls, [])
  c.document.visibilityState = 'visible'
  call(c, 'restoreVisibility')
  await new Promise(setImmediate)
  assert.deepEqual(c.members.map((m) => m.id), ['new'])
  assert.deepEqual(c.choices, ['@new'])
  assert.equal(c.questions[0]?.id, 'q-new')
  assert.equal(c.pins[0]?.id, 'p-new')
  assert.equal(c.syncStatus['/r']?.origin.behind, 2)
  assert.equal(c.issues[0]?.id, 'i-new')
  assert.deepEqual(calls.sort(), ['issues', 'issues', 'members', 'mentions', 'pins', 'questions', 'rooms', 'sync'])
  c.roomData?.destroy()
})

// ComputersApp is the state the roster and computer handlers read.
interface ComputersApp extends AppState {
  roomId: string
  roomRequest: number
  view: string
  drill: null
  memberChips: boolean
  hierarchy: Member[]
  members: Member[]
  showClosed: boolean
  pickedSessions: unknown[]
  feedViews: { resolveView(value: string): string }
  computerRequest: number
  computers: Computer[]
  computerFeed: unknown
  computerError: string
  reconcileRows: typeof reconcileRows
  applyEvent: typeof applyEvent
  openedApps: typeof openedApps
  pushAction: typeof pushAction
  reload?: ReturnType<typeof createReloadGate>
  api: { computers(): Promise<Computer[]> }
}

test('a channel roster change refreshes computers once and its later computer event updates the visible list', async () => {
  const one = member('one'), two = member('two')
  let computerRows: Computer[] = [{ session: 'one', name: 'one', state: 'none' }], calls = 0
  const c: ComputersApp = {
    roomId: 'a', roomRequest: 1, view: 'channel:general', drill: null, memberChips: true,
    hierarchy: [one], members: [one], showClosed: false, pickedSessions: [],
    feedViews: { resolveView: (value: string) => value }, setPicked() {}, pruneSelection: () => [],
    soonRooms() {}, computerRequest: 0, computers: [...computerRows], computerFeed: {}, computerError: '',
    reconcileRows, applyEvent, openedApps, pushAction,
    api: { computers: async () => { calls++; return computerRows } },
  }
  c.reload = createReloadGate(() => ({ room: c.roomId, visit: c.roomRequest }), (name: string) => listVisible(name, c))
  // The member rows live in the room loader now; the computers stay in App.
  installRoomLoader(c, c.api)
  appFunctions(c, ['loadComputers', 'handleComputer'])
  computerRows = [...computerRows, { session: 'two', name: 'two', state: 'none' }]
  call(c, 'applyMembers', [one, two])
  await new Promise(setImmediate)
  assert.equal(calls, 1)
  assert.equal(c.computers.some((row) => row.session === 'two'), true)
  call(c, 'handleComputer', { session: 'two', type: 'started', computer: { session: 'two', state: 'running', apps: [] } })
  assert.equal(c.computers.filter((row) => row.state === 'running').length, 1)
  call(c, 'applyMembers', [{ ...one, state: 'live' }, two])
  await new Promise(setImmediate)
  assert.equal(calls, 1, 'status-only member changes must not reload computers')
  computerRows = [need(computerRows[0], 'first computer row')]
  call(c, 'applyMembers', [one])
  await new Promise(setImmediate)
  assert.equal(calls, 2)
  assert.equal(c.computers.some((row) => row.session === 'two'), false)
})

// BurstApp is the state the session event handlers read.
interface BurstApp extends AppState {
  reviewVersion: number
  roomId: string
  roomRequest: number
  drill: { id: string } | null
  home: null
  feed: null
  roomSessionController: null
  openRun: string
  sessionData?: ReturnType<typeof createSessionData>
}

test('App session event bursts share transcript and queue requests through the reload gate', async () => {
  const waits = [deferred<unknown>(), deferred<unknown>()]
  let transcripts = 0, queues = 0
  const c: BurstApp = {
    reviewVersion: 0,
    roomId: 'a', roomRequest: 1, drill: { id: 'one' }, home: null, feed: null,
    roomSessionController: null, openRun: '', soonRooms() {},
    loadMembers() {}, loadConsolidations() {},
  }
  c.sessionData = sessionDataFor({
    transcriptPage: () => waits[transcripts++]?.promise,
    sessionQueue: async () => { queues++; return [] },
  }, {
    current: () => ({ room: c.roomId, visit: c.roomRequest, id: c.drill?.id, selection: 1, turn: 0, limit: 6 }),
    transcript() {}, queue() {}, error() {},
  })
  appFunctions(c, ['loadSession', 'handle'])
  const initial = call(c, 'loadSession')
  for (let i = 0; i < 10; i++) call(c, 'handle', { kind: 'session', roomId: 'a', change: { op: 'upsert', id: 'one' } })
  assert.equal(transcripts, 1)
  assert.equal(queues, 1)
  assert.equal(c.reviewVersion, 10, 'session activity updates the review workspace')
  waits[0]?.resolve({})
  await new Promise(setImmediate)
  assert.equal(transcripts, 2)
  assert.equal(queues, 2)
  waits[1]?.resolve({})
  await initial
  need(c.sessionData, 'sessionData').destroy()
})

test('hidden pages skip session polling and discard late transcripts', async (t) => {
  t.mock.timers.enable({ apis: ['setInterval'] })
  let visible = true, calls = 0, applied = 0
  const wait = deferred<unknown>()
  interface HiddenApp extends AppState {
    sessionData: ReturnType<typeof createSessionData>
  }
  const c: HiddenApp = { sessionData: sessionDataFor({
    transcriptPage: () => { calls++; return wait.promise }, sessionQueue: async () => [],
  }, {
    current: () => ({ room: 'a', visit: 1, id: 'one', selection: 1, live: true, visible }),
    transcript: () => applied++, queue() {}, error() {},
  }) }
  appFunctions(c, ['loadSession'])
  const pending = call(c, 'loadSession')
  visible = false
  c.sessionData.start()
  t.mock.timers.tick(3000)
  assert.equal(calls, 1)
  wait.resolve({})
  await pending
  assert.equal(applied, 0)
  c.sessionData.destroy()
})

// PollApp is the state the polling effect reads.
interface PollApp extends AppState {
  roomId: string
  roomRequest: number
  view: string
  drill: null
  memberChips: boolean
  pageVisible: boolean
  needPair: boolean
  bulkControllers: Map<string, unknown>
  setInterval: (fn: () => void) => () => void
  reconcileRows: typeof reconcileRows
  reload?: ReturnType<typeof createReloadGate>
  roomSessionController?: ReturnType<typeof createRoomSessionControls>
  api: {
    sessionConsolidations(): Promise<unknown[]>
    roomSessionControls(): Promise<{ roomId: string; paused: boolean; running: number; held: number; dormant: number }>
    pauseRoomSessions(): Promise<unknown>
    resumeRoomSessions(): Promise<unknown>
    clearDormantSessions(): Promise<unknown>
  }
}

test('App polling leaves visible and hidden control panels unchanged', async () => {
  const calls: string[] = []
  const c: PollApp = {
    roomId: 'a', roomRequest: 1, view: 'github', drill: null, memberChips: false, pageVisible: true, needPair: false,
    bulkControllers: new Map(), setInterval: (fn: () => void) => fn, reconcileRows,
    api: {
      sessionConsolidations: async () => { calls.push('consolidations'); return [] },
      roomSessionControls: async () => {
        calls.push('controls'); return { roomId: 'a', paused: false, running: 0, held: 0, dormant: 0 }
      },
      pauseRoomSessions: async () => ({}), resumeRoomSessions: async () => ({}), clearDormantSessions: async () => ({}),
    },
  }
  c.reload = createReloadGate(() => ({ room: c.roomId, visit: c.roomRequest }), (name: string) => listVisible(name, c))
  c.roomSessionController = createRoomSessionControls(c.api, 'a', () => {}, () => listVisible('roomControls', c))
  c.refreshHeldComments ??= () => {}
  vm.createContext(c)
  const transferPoll = initOf(onMountBody(), 'transferPoll')
  if (!transferPoll || !isCallTo(transferPoll, 'setInterval')) throw new Error('App.svelte has no transferPoll')
  const callback = transferPoll.arguments[0]
  if (!callback) throw new Error('App.svelte has no transfer poll callback')
  const poll = runExpression(c, callback)
  if (typeof poll !== 'function') throw new Error('transferPoll callback is not a function')
  poll()
  await new Promise(setImmediate)
  assert.deepEqual(calls, [])
  c.memberChips = true
  poll()
  await new Promise(setImmediate)
  assert.deepEqual(calls, [])
  need(c.roomSessionController, 'roomSessionController').destroy()
})

// SessionApp is the state the session loader and the tab effect read.
interface SessionApp extends AppState {
  roomId: string
  roomRequest: number
  drill: { id: string }
  sessionVisit: number
  narrow: boolean
  reviewOpen: boolean
  sessionTurn: number
  sessionReadingHistory: boolean
  mergeSessionPage: typeof mergeSessionPage
  sessionTab: string
  pageVisible: boolean
  session: { member?: { id?: string }; turns?: { text?: string }[] } | null
  sessionError: string
  queue: unknown[]
  sessionData?: ReturnType<typeof createSessionData>
}

function sessionApp(tab: string, api: unknown) {
  let effect: () => void = () => {}
  const c: SessionApp = {
    roomId: 'a', roomRequest: 1, drill: { id: 'one' }, sessionVisit: 1,
    narrow: false, reviewOpen: false, sessionTurn: 0, sessionReadingHistory: false, mergeSessionPage, sessionTab: tab, pageVisible: true,
    session: null, sessionError: '', queue: [],
    untrack: (fn: () => void) => fn(), $effect: (fn: () => void) => { effect = fn },
  }
  // The transcript and its controller live in lib/app/sessionView.svelte.ts now.
  // The test builds the controller over the same state and gives it to the module.
  const data = sessionDataFor(api, {
    current: () => ({ room: c.roomId, visit: c.roomRequest, id: c.drill?.id, root: false, selection: String(c.sessionVisit ?? 0), turn: 0, visible: !!call(c, 'sessionVisible', c.sessionTab) }),
    transcript: (value: unknown, older: unknown) => call(c, 'showSession', value, older),
    queue: (value: unknown[]) => { c.queue = value },
    error: (error: string) => { c.sessionError = error },
  })
  const view = installSessionView(c, data)
  view.loadSession
  c.sessionData = data
  const entry = findStatement('$effect', 'const tab = sessionTab')
  if (!entry) throw new Error('App.svelte has no session tab effect')
  runExpression(c, entry)
  return { c, effect: () => effect() }
}

for (const tab of ['computer', 'media']) test(`the ${tab} tab receives new messages through the shared App transcript loader`, async () => {
  let reply = 'first message'
  const { c, effect } = sessionApp(tab, {
    transcriptPage: async () => ({ member: { id: 'one', state: 'live' }, turns: [{ text: reply }] }),
    sessionQueue: async () => [],
  })
  effect()
  await new Promise(setImmediate)
  assert.equal(c.session?.turns?.[0]?.text, 'first message')
  reply = 'new reply'
  await call(c, 'loadSession')
  assert.equal(c.session?.turns?.[0]?.text, 'new reply')
  need(c.sessionData, 'sessionData').destroy()
})

test('returning to chat reloads a discarded first transcript without a room event', async () => {
  const first = deferred<unknown>()
  let calls = 0
  const { c, effect } = sessionApp('chat', {
    transcriptPage: () => ++calls === 1 ? first.promise : Promise.resolve({ member: { id: 'one' }, turns: [] }),
    sessionQueue: async () => [],
  })
  const pending = call(c, 'loadSession')
  c.sessionTab = 'prs'
  effect()
  first.resolve({ member: { id: 'old' }, turns: [] })
  await pending
  const discarded = c.session
  assert.equal(discarded, null)
  c.sessionTab = 'chat'
  effect()
  await new Promise(setImmediate)
  assert.equal(calls, 2)
  assert.equal(c.session?.member?.id, 'one')
  need(c.sessionData, 'sessionData').destroy()
})

// FeedApp is the chat state the post handler reads.
interface FeedApp extends AppState {
  home: null
  drill: null
  roomId: string
  roomSessionController: null
  feedAtBottom: boolean
  feed: { room: { id: string }; posts: { id: string }[]; newer?: number }
}

test('live posts mark a missing newer page while the reader is in history', () => {
  const c: FeedApp = {
    home: null, drill: null, roomId: 'room', roomSessionController: null, feedAtBottom: false,
    feed: { room: { id: 'room' }, posts: [{ id: 'old' }] }, soonRooms() {}, loadVisuals() {},
  }
  // A live page joins the feed through the room loader.
  installRoomLoader(c, {})
  appFunctions(c, ['handle'])
  for (let i = 0; i < 1000; i++) call(c, 'handle', { kind: 'posts', roomId: 'room', posts: [{ id: 'new' + i }] })
  assert.deepEqual(c.feed.posts.map((p) => p.id), ['old'])
  assert.equal(c.feed.newer, 1000)
})
