import { hasItemChange, patchListRows } from './listChanges.ts'
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { mergeListPage } from './listPages.ts'
import { createReloadGate, listVisible, reconcileRows } from './reloadGate.ts'
import { reloadsUnread } from './unreadReload.ts'

const deferred = () => { let resolve; const promise = new Promise((r) => { resolve = r }); return { promise, resolve } }

test('ten rapid calls share one request and one trailing reload', async () => {
  const waits = [deferred(), deferred()], applied = []
  let calls = 0
  const gate = createReloadGate(() => ({ room: 'a', visit: 1 }))
  const load = () => gate('members', async (current) => {
    const value = await waits[calls++].promise
    if (current()) applied.push(value)
  })
  const all = Array.from({ length: 10 }, load)
  assert.equal(calls, 1)
  waits[0].resolve('first')
  await new Promise(setImmediate)
  assert.equal(calls, 2)
  waits[1].resolve('last')
  await Promise.all(all)
  assert.deepEqual(applied, ['first', 'last'])
})

test('closed rooms drop answers and trailing reloads', async () => {
  let selection = { room: 'a', visit: 1 }
  const wait = deferred(), applied = []
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
  const gate = createReloadGate(() => state), wait = deferred(), applied = []
  const old = gate('members', async (current) => {
    await wait.promise
    if (current()) applied.push('a')
  })
  state = { room: 'b', visit: 2 }
  await gate('members', async (current) => { if (current()) applied.push('b') })
  wait.resolve(); await old
  assert.deepEqual(applied, ['b'])
})

test('App PR events use the real gated loaders and skip the closed PR view', async () => {
  const { readFile } = await import('node:fs/promises')
  const { parse } = await import('svelte/compiler')
  const vm = await import('node:vm')
  const app = await readFile(new URL('../../App.svelte', import.meta.url), 'utf8')
  const calls = []
  const c = {
    mergeVersion: 0, roomId: 'a', roomRequest: 1, pageVisible: true, view: 'channel:general', drill: null,
    consolidationAlive: true, changes: null, prError: '', reviewer: null,
    home: null, feed: null, hasItemChange, reloadsUnread, reconcileRows, mergeListPage, listPages: {}, soonRooms() {}, reviewerSoon() {},
    api: {
      changes: async () => { calls.push('changes'); return { items: [], older: false, cursor: '' } },
      reviewerStatus: async () => null,
    },
  }
  c.reload = createReloadGate(() => ({ room: c.roomId, visit: c.roomRequest }), (name) => listVisible(name, c))
  c.refreshHeldComments ??= () => {}
  vm.createContext(c)
  const declarations = parse(app).instance.content.body
  for (const name of ['applyListItem', 'applyListPage', 'loadReviewer', 'loadChanges', 'applyItemEvent', 'handle']) {
    const node = declarations.find((n) => n.type === 'FunctionDeclaration' && n.id.name === name)
    vm.runInContext(app.slice(node.start, node.end), c)
  }
  c.handle({ kind: 'prs', roomId: 'a' })
  c.handle({ kind: 'changes', roomId: 'a' })
  await new Promise(setImmediate)
  assert.deepEqual(calls, [])
  c.view = 'github'
  c.handle({ kind: 'prs', roomId: 'a' })
  c.handle({ kind: 'changes', roomId: 'a' })
  await new Promise(setImmediate)
  assert.deepEqual(calls, ['changes'])
  assert.equal(c.prError, '')
  calls.length = 0
  c.view = 'channel:general'
  c.showRoomSettings = true
  c.loadGoals = () => {}
  c.loadRules = () => c.reload('rules', async () => { calls.push('rules') })
  c.handle({ kind: 'goals', roomId: 'a' })
  await new Promise(setImmediate)
  assert.deepEqual(calls, ['rules'])
  calls.length = 0
  c.showRoomSettings = false
  c.handle({ kind: 'goals', roomId: 'a' })
  await new Promise(setImmediate)
  assert.deepEqual(calls, [])
})

test('PR events never load TrackedPRs, and a burst loads the reviewer once', async () => {
  const { readFile } = await import('node:fs/promises')
  const { parse } = await import('svelte/compiler')
  const vm = await import('node:vm')
  const app = await readFile(new URL('../../App.svelte', import.meta.url), 'utf8')
  const calls = []
  const timers = new Map()
  let next = 0
  const c = {
    mergeVersion: 0, roomId: 'a', roomRequest: 1, pageVisible: true, view: 'github', drill: null,
    consolidationAlive: true, changes: null, prError: '', reviewer: null,
    home: null, feed: null, hasItemChange, reloadsUnread, reconcileRows, mergeListPage, listPages: {}, soonRooms() {},
    reviewerTimer: 0, setTimeout: (fn) => { timers.set(++next, fn); return next }, clearTimeout: (id) => timers.delete(id),
    api: {
      trackedPRs: async () => { calls.push('prs'); return { items: [], older: false, cursor: '' } },
      changes: async () => { calls.push('changes'); return { items: [], older: false, cursor: '' } },
      reviewerStatus: async () => { calls.push('reviewer'); return null },
    },
  }
  c.reload = createReloadGate(() => ({ room: c.roomId, visit: c.roomRequest }), (name) => listVisible(name, c))
  c.refreshHeldComments ??= () => {}
  vm.createContext(c)
  const declarations = parse(app).instance.content.body
  for (const name of ['applyListItem', 'applyListPage', 'loadReviewer', 'loadChanges', 'reviewerSoon', 'applyItemEvent', 'handle']) {
    const node = declarations.find((n) => n.type === 'FunctionDeclaration' && n.id.name === name)
    assert.ok(node, name)
    vm.runInContext(app.slice(node.start, node.end), c)
  }
  // Metadata events (reviewer settings, policy) carry no row.
  for (let i = 0; i < 5; i++) c.handle({ kind: 'prs', roomId: 'a' })
  c.handle({ kind: 'changes', roomId: 'a' })
  await new Promise(setImmediate)
  for (const fn of [...timers.values()]) fn()
  await new Promise(setImmediate)
  assert.deepEqual(calls.filter((x) => x !== 'changes'), ['reviewer'])
  assert.equal(calls.filter((x) => x === 'changes').length, 1)
})

test('a Changes load that succeeds after a failure clears the PR error', async () => {
  const { readFile } = await import('node:fs/promises')
  const { parse } = await import('svelte/compiler')
  const vm = await import('node:vm')
  const app = await readFile(new URL('../../App.svelte', import.meta.url), 'utf8')
  let fail = true
  const c = {
    mergeVersion: 0, roomId: 'a', roomRequest: 1, pageVisible: true, view: 'github', drill: null,
    consolidationAlive: true, changes: null, prError: '', listPages: {}, reconcileRows, mergeListPage,
    api: { changes: async () => { if (fail) throw new Error('engine away'); return { items: [], older: false, cursor: '' } } },
  }
  c.reload = createReloadGate(() => ({ room: c.roomId, visit: c.roomRequest }), (name) => listVisible(name, c))
  c.refreshHeldComments ??= () => {}
  vm.createContext(c)
  const declarations = parse(app).instance.content.body
  for (const name of ['applyListPage', 'loadChanges']) {
    const node = declarations.find((n) => n.type === 'FunctionDeclaration' && n.id.name === name)
    vm.runInContext(app.slice(node.start, node.end), c)
  }
  await c.loadChanges()
  assert.equal(c.prError, 'engine away')
  fail = false
  await c.loadChanges()
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
  const key = (v) => v.post.id + ':' + v.attachment.id
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
  const { readFile } = await import('node:fs/promises')
  const { parse } = await import('svelte/compiler')
  const vm = await import('node:vm')
  const app = await readFile(new URL('../../App.svelte', import.meta.url), 'utf8')
  const declarations = parse(app).instance.content.body
  const calls = []
  const c = {
    mergeVersion: 0, roomId: 'a', roomRequest: 1, pageVisible: true, view: 'channel:general', drill: null,
    memberChips: true, sidebarChips: false, showRoomSettings: false,
    consolidationAlive: true, goals: [], changes: null,
    prError: '', loadError: '', reviewer: null, home: null, feed: null,
    hasItemChange, reloadsUnread, reconcileRows, mergeListPage, listPages: {}, soonRooms() {}, reviewerSoon() {}, loadRules() {}, loadVisuals() {},
    untrack: (fn) => fn(), $effect: (fn) => fn(),
    api: {
      changes: async () => { calls.push('changes'); return { items: [], older: false, cursor: '' } },
      goals: async () => { calls.push('goals'); return { items: [], older: false, cursor: '' } },
      reviewerStatus: async () => null,
    },
  }
  c.reload = createReloadGate(() => ({ room: c.roomId, visit: c.roomRequest }), (name) => listVisible(name, c))
  c.fetchedGoal ??= null; c.fetchedGoalVersion ??= 0
  c.refreshHeldComments ??= () => {}
  vm.createContext(c)
  for (const name of ['applyListItem', 'applyListPage', 'loadReviewer', 'loadChanges', 'loadGoals', 'trackFetchedGoal', 'refreshFetchedGoal', 'applyItemEvent', 'handle']) {
    const node = declarations.find((n) => n.type === 'FunctionDeclaration' && n.id.name === name)
    vm.runInContext(app.slice(node.start, node.end), c)
  }
  const entry = declarations.find((n) => n.type === 'ExpressionStatement' &&
    n.expression.callee?.name === '$effect' && app.slice(n.start, n.end).includes('loadChanges()'))
  vm.runInContext(app.slice(entry.start, entry.end), c)
  await new Promise(setImmediate)
  assert.deepEqual(calls, ['changes', 'goals'])
  assert.equal(c.prError, '')
  assert.equal(c.loadError, '')
  calls.length = 0
  c.handle({ kind: 'changes', roomId: 'a' })
  c.handle({ kind: 'goals', roomId: 'a' })
  await new Promise(setImmediate)
  assert.deepEqual(calls, ['changes', 'goals'])
  assert.equal(c.prError, '')
  assert.equal(c.loadError, '')
  calls.length = 0
  c.memberChips = false
  c.view = 'files'
  c.handle({ kind: 'changes', roomId: 'a' })
  c.handle({ kind: 'goals', roomId: 'a' })
  await new Promise(setImmediate)
  assert.deepEqual(calls, [])
})

test('the App visual loader reuses each card by the shared visual identity', async () => {
  const { readFile } = await import('node:fs/promises')
  const { parse } = await import('svelte/compiler')
  const { visualKey } = await import('../rooms/questions.js')
  const vm = await import('node:vm')
  const app = await readFile(new URL('../../App.svelte', import.meta.url), 'utf8')
  let response = [
    { id: 'p1', post: { id: 'p1' }, attachment: { id: 'a', title: 'first' } },
    { id: 'p2', post: { id: 'p2' }, attachment: { id: 'a', title: 'second' } },
  ]
  const c = {
    roomId: 'room', visuals: [], loadError: '', reconcileRows, visualKey, setVisuals: () => {},
    api: { visuals: async () => response },
    reload: createReloadGate(() => ({ room: 'room', visit: 1 })),
  }
  c.refreshHeldComments ??= () => {}
  vm.createContext(c)
  const node = parse(app).instance.content.body.find((n) => n.type === 'FunctionDeclaration' && n.id.name === 'loadVisuals')
  vm.runInContext(app.slice(node.start, node.end), c)
  await c.loadVisuals()
  const first = c.visuals[0], second = c.visuals[1]
  response = [
    { id: 'p1', post: { id: 'p1' }, attachment: { id: 'a', title: 'updated' } },
    { id: 'p2', post: { id: 'p2' }, attachment: { id: 'a', title: 'second' } },
  ]
  await c.loadVisuals()
  assert.equal(c.visuals[0], first)
  assert.equal(c.visuals[1], second)
  assert.notEqual(first, second)
  assert.equal(first.attachment.title, 'updated')
  assert.equal(new Set(c.visuals.map(visualKey)).size, 2)
})

test('item patches survive a request and its trailing refresh', async () => {
  const wait = deferred(), applied = []
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
  const wait = deferred(), applied = []
  const gate = createReloadGate(() => selection)
  const first = gate('goals', () => wait.promise)
  const patch = gate.patch('goals', () => { applied.push('old') })
  selection = { room: 'b', visit: 2 }
  wait.resolve()
  await Promise.all([first, patch])
  assert.deepEqual(applied, [])
})

test('a visual update reaches chat cards while chat stays open', async () => {
  const { readFile } = await import('node:fs/promises')
  const { parse } = await import('svelte/compiler')
  const { visualKey } = await import('../rooms/questions.js')
  const { byVisual, newestVersion } = await import('../files/visuals.ts')
  const vm = await import('node:vm')
  const app = await readFile(new URL('../../App.svelte', import.meta.url), 'utf8')
  const v1 = { id: 'a1', render: 'html', version: 1 }
  const v2 = { id: 'a2', render: 'html', visual: 'p1', version: 2 }
  let response = [{ id: 'p1', post: { id: 'p1' }, attachment: v1, versions: [{ attachment: v1 }] }]
  let shown = {}
  const c = {
    roomId: 'room', roomRequest: 1, view: 'channel:general', drill: null, visuals: [], loadError: '', reconcileRows, visualKey,
    setVisuals: (_, list) => { shown = byVisual(list) },
    api: { visuals: async () => response },
  }
  c.reload = createReloadGate(() => ({ room: c.roomId, visit: c.roomRequest }), (name) => listVisible(name, c))
  c.refreshHeldComments ??= () => {}
  vm.createContext(c)
  const node = parse(app).instance.content.body.find((n) => n.type === 'FunctionDeclaration' && n.id.name === 'loadVisuals')
  vm.runInContext(app.slice(node.start, node.end), c)
  await c.loadVisuals()
  assert.equal(newestVersion(shown, 'p1', v1), v1)
  response = [{ id: 'p1', post: { id: 'p2' }, attachment: v2, versions: [{ attachment: v2 }, { attachment: v1 }] }]
  await c.loadVisuals()
  assert.equal(newestVersion(shown, 'p1', v1).id, 'a2', 'the v1 card in chat now shows v2')
})

test('Issues skips PR data unless visible PR chips need it', async () => {
  const state = { room: 'a', visit: 1, view: 'issues' }
  const gate = createReloadGate(() => state, (name) => listVisible(name, state))
  const calls = []
  const refresh = () => Promise.all(['changes', 'reviewer'].map((name) => gate(name, async () => { calls.push(name) })))
  await refresh()
  assert.deepEqual(calls, [])
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
 const changed={id:'changed',state:'idle'}
 const previous=[unchanged,changed]
 const result=patchListRows(previous,{op:'upsert',id:'changed',item:{id:'changed',state:'live'}})
 assert.equal(changed.state,'live')
 assert.equal(result,previous)
 assert.equal(enumerations,0)
})

test('an identical row with non-serializable view data needs no field comparison', () => {
 const detail={};detail.self=detail
 const row={id:'same',detail}
 const previous=[row]
 assert.equal(reconcileRows(previous,[row]),previous)
})

test('PR and agent badges receive goal updates with the members panel closed', async () => {
  const state = { room: 'a', visit: 1, view: 'github', drill: null, memberChips: false, sidebarChips: false }
  const gate = createReloadGate(() => state, (name) => listVisible(name, state))
  const updated = []
  for (const patch of [{ view: 'github' }, { view: 'files', drill: { id: 'worker' } }, { drill: null, sidebarChips: true }]) {
    Object.assign(state, patch)
    await gate.patch('goals', (current) => { if (current()) updated.push(state.view) })
  }
  assert.deepEqual(updated, ['github', 'files', 'files'])
})
