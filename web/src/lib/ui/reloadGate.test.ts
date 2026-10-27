import { errorText } from '../errorText.ts'
import { hasItemChange, patchListRows } from './listChanges.ts'
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { parse } from 'svelte/compiler'
import vm from 'node:vm'
import { mergeListPage } from './listPages.ts'
import { createReloadGate, listVisible, reconcileRows } from './reloadGate.ts'
import { PRS_FUNCTIONS, QUESTIONS_FUNCTIONS, installGoals, installPRs, installQuestions } from '../../../test/appModules.ts'
import { reloadsUnread } from './unreadReload.ts'
import { topLevelFunctions, sourceRange, stripTypes } from '../../../test/svelte-source.ts'
import { need } from '../../../test/need.ts'

// A promise the test settles by hand.
function deferred<T = void>() {
  let resolve: (value: T) => void = () => {}
  const promise = new Promise<T>((r) => { resolve = r })
  return { promise, resolve }
}

test('ten rapid calls share one request and one trailing reload', async () => {
  const waits = [deferred<string>(), deferred<string>()], applied: string[] = []
  let calls = 0
  const gate = createReloadGate(() => ({ room: 'a', visit: 1 }))
  const load = () => gate('members', async (current) => {
    const value = await need(waits[calls++]).promise
    if (current()) applied.push(value)
  })
  const all = Array.from({ length: 10 }, load)
  assert.equal(calls, 1)
  need(waits[0]).resolve('first')
  await new Promise(setImmediate)
  assert.equal(calls, 2)
  need(waits[1]).resolve('last')
  await Promise.all(all)
  assert.deepEqual(applied, ['first', 'last'])
})

test('closed rooms drop answers and trailing reloads', async () => {
  let selection = { room: 'a', visit: 1 }
  const wait = deferred(), applied: string[] = []
  let calls = 0
  const gate = createReloadGate(() => selection)
  const load = () => gate('goals', async (current) => {
    calls++
    await wait.promise
    if (current()) applied.push('old')
  })
  const pending = load(); load()
  selection = { room: 'b', visit: 2 }
  wait.resolve()
  await pending
  assert.equal(calls, 1)
  assert.deepEqual(applied, [])
})

test('a changes event cannot fetch a closed PR view', async () => {
  const state = { room: 'a', visit: 1, view: 'channel:general' }
  const gate = createReloadGate(() => state, (name) => listVisible(name, state))
  let calls = 0
  const onEvent = () => gate('changes', async () => { calls++ })
  await onEvent()
  assert.equal(calls, 0)
  state.view = 'github'
  await onEvent()
  assert.equal(calls, 1)
})

test('row refreshes preserve objects and update only changed fields', () => {
  const first = { id: 'a', name: 'same' }, second = { id: 'b', name: 'old' }
  const rows = reconcileRows([first, second], [{ ...first }, { id: 'b', name: 'new' }])
  assert.equal(rows[0], first)
  assert.equal(rows[1], second)
  assert.equal(second.name, 'new')
  assert.equal(reconcileRows(rows, [{ ...first }, { ...second }]), rows)
})

test('closing a view drops its late response and queued refresh', async () => {
  const state = { room: 'a', visit: 1, view: 'github' }
  const gate = createReloadGate(() => state, (name) => listVisible(name, state))
  const wait = deferred()
  let calls = 0, applied = 0
  const load = () => gate('changes', async (current) => {
    calls++
    await wait.promise
    if (current()) applied++
  })
  const pending = load(); load()
  state.view = 'goals'
  wait.resolve()
  await pending
  assert.equal(calls, 1)
  assert.equal(applied, 0)
})

test('a new room can load while the old room request finishes', async () => {
  let state = { room: 'a', visit: 1 }
  const gate = createReloadGate(() => state), wait = deferred(), applied: string[] = []
  const old = gate('members', async (current) => {
    await wait.promise
    if (current()) applied.push('a')
  })
  state = { room: 'b', visit: 2 }
  await gate('members', async (current) => { if (current()) applied.push('b') })
  wait.resolve(); await old
  assert.deepEqual(applied, ['b'])
})

// The App state these tests drive. A member of the state that a test never
// names still needs roomId and view, because listVisible reads them.
interface Sandbox extends Record<string, unknown> {
  roomId: string
  drill?: Partial<{ id: string }> | null
  memberChips?: boolean
  sidebarChips?: boolean
  showRoomSettings?: boolean
  prError?: string
  loadError?: string
}

// A card row of the Visuals list.
interface Visual {
  id: string
  render?: string
  version?: number
  visual?: string
  title?: string
}

interface VisualRow {
  id: string
  post: { id: string }
  attachment: Visual
  versions?: { attachment: Visual }[]
}

// listVisible reads the view, so a sandbox that gates a list names it.
interface VisibleSandbox extends Sandbox {
  view: string
}

interface VisualSandbox extends Sandbox {
  visuals: VisualRow[]
}

interface ChatVisualSandbox extends VisualSandbox, VisibleSandbox {}

// The App functions arrive from the vm, which has no type to report.
function callFunction<A extends unknown[], R>(sandbox: Record<string, unknown>, name: string, ...args: A): R {
  const fn = sandbox[name]
  if (typeof fn !== 'function') throw new Error(`App.svelte defines no ${name}`)
  return fn.apply(sandbox, args) as R
}

const appSource = () => readFile(new URL('../../App.svelte', import.meta.url), 'utf8')

// The PR and visual lists moved to lib/app modules; a test that names one of
// their loaders installs that module over the same sandbox.
const MODULES: readonly [readonly string[], (state: object) => void][] = [
  [PRS_FUNCTIONS, installPRs],
  [QUESTIONS_FUNCTIONS, installQuestions],
]

// runApp installs the named App functions and module loaders on the sandbox.
function runApp(source: string, c: Record<string, unknown>, names: readonly string[]): void {
  vm.createContext(c)
  const moved = new Set<string>()
  for (const [functions, install] of MODULES) {
    if (!functions.some((name) => names.includes(name))) continue
    install(c)
    for (const name of functions) moved.add(name)
  }
  const kept = names.filter((name) => !moved.has(name))
  const found = topLevelFunctions(source, kept)
  for (const name of kept) assert.ok(found.has(name), name)
  for (const code of found.values()) vm.runInContext(code, c)
}

test('App PR events use the real gated loaders and skip the closed PR view', async () => {
  const app = await appSource()
  const calls: string[] = []
  const c: VisibleSandbox = {
    errorText, mergeVersion: 0, roomId: 'a', roomRequest: 1, pageVisible: true, view: 'channel:general', drill: null,
    consolidationAlive: true, changes: null, prError: '', reviewer: null,
    home: null, feed: null, hasItemChange, reloadsUnread, reconcileRows, mergeListPage, listPages: {}, soonRooms() {}, reviewerSoon() {}, loadIssues() {},
    api: {
      changes: async () => { calls.push('changes'); return { items: [], older: false, cursor: '' } },
      reviewerStatus: async () => null,
    },
  }
  const reload = createReloadGate(() => ({ room: c.roomId, visit: c.roomRequest }), (name) => listVisible(name, c))
  c.reload = reload
  c.refreshHeldComments ??= () => {}
  runApp(app, c, ['applyListItem', 'applyListPage', 'loadReviewer', 'loadChanges', 'applyItemEvent', 'handle'])
  callFunction(c, 'handle', { kind: 'prs', roomId: 'a' })
  callFunction(c, 'handle', { kind: 'changes', roomId: 'a' })
  await new Promise(setImmediate)
  assert.deepEqual<string[]>(calls, [])
  c.view = 'github'
  callFunction(c, 'handle', { kind: 'prs', roomId: 'a' })
  callFunction(c, 'handle', { kind: 'changes', roomId: 'a' })
  await new Promise(setImmediate)
  assert.deepEqual(calls, ['changes'])
  assert.equal(c.prError, '')
  calls.length = 0
  c.view = 'channel:general'
  c.showRoomSettings = true
  c.loadGoals = () => {}
  c.loadRules = () => reload('rules', async () => { calls.push('rules') })
  callFunction(c, 'handle', { kind: 'goals', roomId: 'a' })
  await new Promise(setImmediate)
  assert.deepEqual(calls, ['rules'])
  calls.length = 0
  c.showRoomSettings = false
  callFunction(c, 'handle', { kind: 'goals', roomId: 'a' })
  await new Promise(setImmediate)
  assert.deepEqual<string[]>(calls, [])
})

test('PR events never load TrackedPRs, and a burst loads the reviewer once', async () => {
  const app = await appSource()
  const calls: string[] = []
  const timers = new Map<number, () => void>()
  let next = 0
  const c: VisibleSandbox = {
    errorText, mergeVersion: 0, roomId: 'a', roomRequest: 1, pageVisible: true, view: 'github', drill: null,
    consolidationAlive: true, changes: null, prError: '', reviewer: null,
    home: null, feed: null, hasItemChange, reloadsUnread, reconcileRows, mergeListPage, listPages: {}, soonRooms() {},
    loadIssues() {},
    reviewerTimer: 0, setTimeout: (fn: () => void) => { timers.set(++next, fn); return next }, clearTimeout: (id: number) => timers.delete(id),
    api: {
      trackedPRs: async () => { calls.push('prs'); return { items: [], older: false, cursor: '' } },
      changes: async () => { calls.push('changes'); return { items: [], older: false, cursor: '' } },
      reviewerStatus: async () => { calls.push('reviewer'); return null },
    },
  }
  c.reload = createReloadGate(() => ({ room: c.roomId, visit: c.roomRequest }), (name) => listVisible(name, c))
  c.refreshHeldComments ??= () => {}
  runApp(app, c, ['applyListItem', 'applyListPage', 'loadReviewer', 'loadChanges', 'reviewerSoon', 'applyItemEvent', 'handle'])
  // Metadata events (reviewer settings, policy) carry no row.
  for (let i = 0; i < 5; i++) callFunction(c, 'handle', { kind: 'prs', roomId: 'a' })
  callFunction(c, 'handle', { kind: 'changes', roomId: 'a' })
  await new Promise(setImmediate)
  for (const fn of [...timers.values()]) fn()
  await new Promise(setImmediate)
  assert.deepEqual(calls.filter((x) => x !== 'changes'), ['reviewer'])
  assert.equal(calls.filter((x) => x === 'changes').length, 1)
})

test('a Changes load that succeeds after a failure clears the PR error', async () => {
  const app = await appSource()
  let fail = true
  const c: VisibleSandbox = {
    errorText, mergeVersion: 0, roomId: 'a', roomRequest: 1, pageVisible: true, view: 'github', drill: null,
    consolidationAlive: true, changes: null, prError: '', listPages: {}, reconcileRows, mergeListPage,
    api: { changes: async () => { if (fail) throw new Error('engine away'); return { items: [], older: false, cursor: '' } } },
  }
  c.reload = createReloadGate(() => ({ room: c.roomId, visit: c.roomRequest }), (name) => listVisible(name, c))
  c.refreshHeldComments ??= () => {}
  runApp(app, c, ['applyListPage', 'loadChanges'])
  await callFunction<[], Promise<void>>(c, 'loadChanges')
  assert.equal(c.prError, 'engine away')
  fail = false
  await callFunction<[], Promise<void>>(c, 'loadChanges')
  assert.equal(c.prError, '')
})

test('a session view and member chips load Changes for their PR rows', () => {
  assert.equal(listVisible('changes', { view: 'channel:general', drill: { id: 's' } }), true)
  assert.equal(listVisible('changes', { view: 'channel:general', drill: null, memberChips: true }), true)
  assert.equal(listVisible('changes', { view: 'channel:general', drill: null, sidebarChips: true }), true)
  assert.equal(listVisible('changes', { view: 'files', drill: null }), false)
})

test('two visual cards keep distinct objects across refreshes with composite keys', () => {
  const first = { post: { id: 'p1' }, attachment: { id: 'card', title: 'first' } }
  const second = { post: { id: 'p2' }, attachment: { id: 'card', title: 'second' } }
  const key = (v: { post: { id: string }; attachment: { id: string } }) => v.post.id + ':' + v.attachment.id
  const rows = reconcileRows([first, second], [
    { post: { id: 'p1' }, attachment: { id: 'card', title: 'updated' } },
    { post: { id: 'p2' }, attachment: { id: 'card', title: 'second' } },
  ], key)
  assert.equal(rows[0], first)
  assert.equal(rows[1], second)
  assert.notEqual(rows[0], rows[1])
  assert.equal(first.attachment.title, 'updated')
  assert.equal(new Set(rows.map(key)).size, 2)
})

test('visible PR and goal chips load in channel views', () => {
  const channel = { view: 'channel:general', drill: null, memberChips: true }
  assert.equal(listVisible('changes', channel), true)
  assert.equal(listVisible('goals', channel), true)
  assert.equal(listVisible('changes', { ...channel, memberChips: false, sidebarChips: true }), true)
  assert.equal(listVisible('changes', { ...channel, memberChips: false, sidebarChips: false }), false)
})

test('room entry and channel events refresh visible PR and goal chips', async () => {
  const app = await appSource()
  const calls: string[] = []
  const c: VisibleSandbox = {
    errorText, mergeVersion: 0, roomId: 'a', roomRequest: 1, pageVisible: true, view: 'channel:general', drill: null,
    memberChips: true, sidebarChips: false, showRoomSettings: false,
    consolidationAlive: true, goals: [], changes: null,
    prError: '', loadError: '', reviewer: null, home: null, feed: null,
    hasItemChange, reloadsUnread, reconcileRows, mergeListPage, listPages: {}, soonRooms() {}, reviewerSoon() {}, loadIssues() {}, loadRules() {}, loadVisuals() {},
    untrack: (fn: () => void) => fn(), $effect: (fn: () => void) => fn(),
    api: {
      changes: async () => { calls.push('changes'); return { items: [], older: false, cursor: '' } },
      goals: async () => { calls.push('goals'); return { items: [], older: false, cursor: '' } },
      reviewerStatus: async () => null,
    },
  }
  c.reload = createReloadGate(() => ({ room: c.roomId, visit: c.roomRequest }), (name) => listVisible(name, c))
  c.fetchedGoal ??= null; c.fetchedGoalVersion ??= 0
  c.refreshHeldComments ??= () => {}
  runApp(app, c, ['applyListItem', 'applyListPage', 'loadReviewer', 'loadChanges', 'applyItemEvent', 'handle'])
  // The room's goals live in lib/app/goals.svelte.ts now; their functions run
  // here over the same state.
  installGoals(c)
  const declarations = parse(app, { modern: true }).instance?.content.body ?? []
  const entry = declarations.find((n) => {
    if (n.type !== 'ExpressionStatement' || n.expression.type !== 'CallExpression') return false
    if (n.expression.callee.type !== 'Identifier' || n.expression.callee.name !== '$effect') return false
    const range = sourceRange(n)
    return app.slice(range.start, range.end).includes('loadChanges()')
  })
  if (!entry) throw new Error('App.svelte has no $effect that loads the changes list')
  const range = sourceRange(entry)
  vm.runInContext(stripTypes(app.slice(range.start, range.end)), c)
  await new Promise(setImmediate)
  assert.deepEqual(calls, ['changes', 'goals'])
  assert.equal(c.prError, '')
  assert.equal(c.loadError, '')
  calls.length = 0
  callFunction(c, 'handle', { kind: 'changes', roomId: 'a' })
  callFunction(c, 'handle', { kind: 'goals', roomId: 'a' })
  await new Promise(setImmediate)
  assert.deepEqual(calls, ['changes', 'goals'])
  assert.equal(c.prError, '')
  assert.equal(c.loadError, '')
  calls.length = 0
  c.memberChips = false
  c.view = 'files'
  callFunction(c, 'handle', { kind: 'changes', roomId: 'a' })
  callFunction(c, 'handle', { kind: 'goals', roomId: 'a' })
  await new Promise(setImmediate)
  assert.deepEqual<string[]>(calls, [])
})

test('the App visual loader reuses each card by the shared visual identity', async () => {
  const app = await appSource()
  const { visualKey } = await import('../requests/requests.ts')
  let response: VisualRow[] = [
    { id: 'p1', post: { id: 'p1' }, attachment: { id: 'a', title: 'first' } },
    { id: 'p2', post: { id: 'p2' }, attachment: { id: 'a', title: 'second' } },
  ]
  const c: VisualSandbox = {
    roomId: 'room', visuals: [], loadError: '', reconcileRows, visualKey, setVisuals: () => {},
    api: { visuals: async () => response },
    reload: createReloadGate(() => ({ room: 'room', visit: 1 })),
  }
  c.refreshHeldComments ??= () => {}
  runApp(app, c, ['loadVisuals'])
  await callFunction<[], Promise<void>>(c, 'loadVisuals')
  const first = need(c.visuals[0]), second = need(c.visuals[1])
  response = [
    { id: 'p1', post: { id: 'p1' }, attachment: { id: 'a', title: 'updated' } },
    { id: 'p2', post: { id: 'p2' }, attachment: { id: 'a', title: 'second' } },
  ]
  await callFunction<[], Promise<void>>(c, 'loadVisuals')
  assert.equal(c.visuals[0], first)
  assert.equal(c.visuals[1], second)
  assert.notEqual(first, second)
  assert.equal(first.attachment.title, 'updated')
  assert.equal(new Set(c.visuals.map(visualKey)).size, 2)
})

test('item patches survive a request and its trailing refresh', async () => {
  const wait = deferred(), applied: string[] = []
  const gate = createReloadGate(() => ({ room: 'a', visit: 1 }))
  const first = gate('goals', async () => { applied.push('fetch'); await wait.promise; applied.push('response') })
  const one = gate.patch('goals', (current) => { if (current()) applied.push('patch-one') })
  const two = gate.patch('goals', (current) => { if (current()) applied.push('patch-two') })
  const refresh = gate('goals', () => { applied.push('reload') })
  wait.resolve()
  await Promise.all([first, one, two, refresh])
  assert.deepEqual(applied, ['fetch', 'response', 'reload', 'patch-one', 'patch-two'])
})

test('a room change drops pending item patches', async () => {
  let selection = { room: 'a', visit: 1 }
  const wait = deferred(), applied: string[] = []
  const gate = createReloadGate(() => selection)
  const first = gate('goals', () => wait.promise)
  const patch = gate.patch('goals', () => { applied.push('old') })
  selection = { room: 'b', visit: 2 }
  wait.resolve()
  await Promise.all([first, patch])
  assert.deepEqual(applied, [])
})

test('a visual update reaches chat cards while chat stays open', async () => {
  const app = await appSource()
  const { visualKey } = await import('../requests/requests.ts')
  const { byVisual, newestVersion } = await import('../files/visuals.ts')
  const v1: Visual = { id: 'a1', render: 'html', version: 1 }
  const v2: Visual = { id: 'a2', render: 'html', visual: 'p1', version: 2 }
  let response: VisualRow[] = [{ id: 'p1', post: { id: 'p1' }, attachment: v1, versions: [{ attachment: v1 }] }]
  let shown: Record<string, VisualRow> = {}
  const c: ChatVisualSandbox = {
    roomId: 'room', roomRequest: 1, view: 'channel:general', drill: null, visuals: [], loadError: '', reconcileRows, visualKey,
    setVisuals: (_first: unknown, list: VisualRow[]) => { shown = byVisual(list) },
    api: { visuals: async () => response },
  }
  c.reload = createReloadGate(() => ({ room: c.roomId, visit: c.roomRequest }), (name) => listVisible(name, c))
  c.refreshHeldComments ??= () => {}
  runApp(app, c, ['loadVisuals'])
  await callFunction<[], Promise<void>>(c, 'loadVisuals')
  assert.equal(newestVersion(shown, 'p1', v1), v1)
  response = [{ id: 'p1', post: { id: 'p2' }, attachment: v2, versions: [{ attachment: v2 }, { attachment: v1 }] }]
  await callFunction<[], Promise<void>>(c, 'loadVisuals')
  assert.equal(newestVersion(shown, 'p1', v1).id, 'a2', 'the v1 card in chat now shows v2')
})

test('Issues skips PR data unless visible PR chips need it', async () => {
  const state: { room: string; visit: number; view: string; sidebarChips?: boolean } = { room: 'a', visit: 1, view: 'issues' }
  const gate = createReloadGate(() => state, (name) => listVisible(name, state))
  const calls: string[] = []
  const refresh = () => Promise.all(['changes', 'reviewer'].map((name) => gate(name, async () => { calls.push(name) })))
  await refresh()
  assert.deepEqual<string[]>(calls, [])
  state.view = 'github'
  await refresh()
  assert.deepEqual(calls, ['changes', 'reviewer'])
  calls.length = 0
  state.view = 'issues'
  state.sidebarChips = true
  await refresh()
  assert.deepEqual(calls, ['changes', 'reviewer'])
})


test('a member delta does not enumerate unchanged proxy rows', () => {
 let enumerations=0
 const unchanged=new Proxy({id:'unchanged',name:'same',detail:{text:'kept'}},{ownKeys(target){enumerations++;return Reflect.ownKeys(target)}})
 const changed: { id: string; state: string } = {id:'changed',state:'idle'}
 const previous=[unchanged,changed]
 const result=patchListRows(previous,{op:'upsert',id:'changed',item:{id:'changed',state:'live'}})
 assert.equal(changed.state,'live')
 assert.equal(result,previous)
 assert.equal(enumerations,0)
})

test('an identical row with non-serializable view data needs no field comparison', () => {
 const detail: { self?: unknown }={};detail.self=detail
 const row={id:'same',detail}
 const previous=[row]
 assert.equal(reconcileRows(previous,[row]),previous)
})

test('PR and agent badges receive goal updates with the members panel closed', async () => {
  const state = { room: 'a', visit: 1, view: 'github', drill: null, memberChips: false, sidebarChips: false }
  const gate = createReloadGate(() => state, (name) => listVisible(name, state))
  const updated: string[] = []
  for (const patch of [{ view: 'github' }, { view: 'files', drill: { id: 'worker' } }, { drill: null, sidebarChips: true }]) {
    Object.assign(state, patch)
    await gate.patch('goals', (current) => { if (current()) updated.push(state.view) })
  }
  assert.deepEqual(updated, ['github', 'files', 'files'])
})
