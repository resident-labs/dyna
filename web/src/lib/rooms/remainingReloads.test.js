import { test } from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { parse } from 'svelte/compiler'
import vm from 'node:vm'
import { changesVisuals } from '../files/visuals.ts'
import { createRoomData } from './roomData.js'
import { mergeSessionPage } from '../sessions/sessionPages.ts'
import { createSessionData } from '../sessions/sessionData.ts'
import { createRoomSessionControls } from './roomSessionControls.js'
import { applyEvent, openedApps, pushAction } from '../computers/computers.js'
import { hasItemChange } from '../ui/listChanges.ts'
import { createReloadGate, listVisible, reconcileRows } from '../ui/reloadGate.ts'
import { reloadsUnread } from '../ui/unreadReload.ts'
import { stripTypes } from '../../../test/svelte-source.ts'

const deferred = () => { let resolve; const promise = new Promise((yes) => { resolve = yes }); return { promise, resolve } }
const source = readFileSync(new URL('../../App.svelte', import.meta.url), 'utf8')
const functions = parse(source).instance.content.body
function appFunctions(c, names) {
  c.hasItemChange = hasItemChange
  c.reloadsUnread = reloadsUnread
  if (names.includes('handle') && !names.includes('applyItemEvent')) names = ['applyItemEvent', ...names]
  c.changesVisuals = changesVisuals
  for (const [k, v] of Object.entries({ mergeVersion: 0, routedParents: "", feed: null, roomId: "" })) if (!(k in c)) c[k] = v
  c.refreshHeldComments ??= () => {}
  vm.createContext(c)
  for (const name of names) {
    const node = functions.find((n) => n.type === 'FunctionDeclaration' && n.id.name === name)
    vm.runInContext(stripTypes(source.slice(node.start, node.end)), c)
  }
}
test('room summary bursts share one request plus one trailing request', async () => {
  const waits = [deferred(), deferred()]
  let calls = 0
  const data = createRoomData({ rooms: () => waits[calls++].promise }, {
    current: () => ({ room: 'a', visit: 1 }), rooms() {}, error() {},
  })
  const requests = Array.from({ length: 10 }, () => data.loadRooms())
  assert.equal(calls, 1)
  waits[0].resolve([])
  await new Promise(setImmediate)
  assert.equal(calls, 2)
  waits[1].resolve([])
  await Promise.all(requests)
  data.destroy()
})
test('member status refreshes do not fetch mention choices for a hidden composer', async () => {
  let mentions = 0
  const data = createRoomData({
    members: async () => [{ id: 'one', name: 'one' }],
    mentions: async () => { mentions++; return ['@one'] },
  }, {
    current: () => ({ room: 'a', visit: 1 }), visible: (name) => name !== 'mentions',
    members() {}, choices() {},
  })
  await data.loadMembers()
  assert.equal(mentions, 0)
  data.destroy()
})
test('session controls skip hidden consumers and merge visible refresh bursts', async () => {
  let visible = false, calls = 0
  const waits = [deferred(), deferred()]
  const control = createRoomSessionControls({ roomSessionControls: () => waits[calls++].promise }, 'a', () => {}, () => visible)
  const hidden = control.load()
  assert.equal(calls, 0)
  await hidden
  visible = true
  const requests = Array.from({ length: 10 }, () => control.load())
  assert.equal(calls, 1)
  waits[0].resolve({ roomId: 'a', paused: false, running: 0, held: 0, dormant: 0 })
  await new Promise(setImmediate)
  assert.equal(calls, 2)
  waits[1].resolve({ roomId: 'a', paused: false, running: 0, held: 0, dormant: 0 })
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
  let members = [{ id: 'one', name: 'one', state: 'idle' }]
  const updates = []
  const data = createRoomData({
    members: async () => members,
    mentions: async () => { calls++; return ['@one'] },
  }, {
    current: () => ({ room: 'a', visit: 1 }),
    visible: (name) => name !== 'mentions' || visible,
    members() {}, choices: (value) => updates.push(value),
  })
  await data.loadMembers()
  assert.equal(calls, 0)
  visible = true
  await data.loadMentions()
  assert.equal(calls, 1)
  members = [{ ...members[0], state: 'live' }]
  await data.loadMembers()
  assert.equal(calls, 1)
  members = [...members, { id: 'two', name: 'two' }]
  await data.loadMembers()
  assert.equal(calls, 2)
  assert.equal(updates.length, 2)
  data.destroy()
})
test('room summaries drop hidden-page responses and queued reloads', async () => {
  let visible = true, calls = 0, updates = 0
  const wait = deferred()
  const data = createRoomData({ rooms: () => { calls++; return wait.promise } }, {
    current: () => ({ room: 'a', visit: 1 }), visible: () => visible,
    rooms: () => updates++, error() {},
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
  const c = {
    roomId: 'a', home: null, feed: null, drill: null, roomSessionController: null, openRun: '',
    soonRooms: (kind = 'rooms') => kind === 'feed' ? feeds++ : kind === 'members' ? members++ : summaries++,
  }
  for (const name of ['loadQuestions', 'loadVisuals', 'loadGoals', 'loadRules', 'loadPins', 'reviewerSoon', 'loadChanges', 'loadMembers', 'loadConsolidations', 'loadRuns']) c[name] = () => {}
  appFunctions(c, ['handle'])
  for (const kind of ['prs', 'goals', 'questions', 'pins', 'runs']) c.handle({ kind, roomId: 'a' })
  assert.equal(summaries, 0)
  // The responsibility counts of the member rows change with these, so they
  // ask for the member kind instead of a room summary.
  assert.equal(members, 4, 'prs, goals, questions and runs reload the member rows')
  for (const kind of ['posts', 'members', 'rooms', 'room.paused', 'room.resumed']) c.handle({ kind, roomId: 'a' })
  assert.equal(summaries, 5)
  assert.equal(feeds, 2, 'posts and members refresh channel counts separately')
})
test('a room snapshot supersedes an older mention request for the same visit', async () => {
  const old = deferred(), updates = []
  const data = createRoomData({
    members: async () => [{ id: 'old', name: 'old' }],
    mentions: () => old.promise,
    openRoom: async () => ({ members: [{ id: 'new', name: 'new' }], choices: ['@new'] }),
  }, {
    current: () => ({ room: 'a', visit: 1 }), visible: () => true,
    members() {}, choices: (value) => updates.push(value), opened: (value) => updates.push(value.choices),
  })
  const pending = data.loadMembers()
  await new Promise(setImmediate)
  await data.open()
  old.resolve(['@old'])
  await pending
  assert.deepEqual(updates.at(-1), ['@new'])
  data.destroy()
})
test('visibility restoration refreshes skipped members, choices, questions and pins without another event', async () => {
  const calls = []
  const c = {
    roomId: 'a', roomRequest: 1, pageVisible: false, started: true,
    view: 'channel:general', drill: null, document: { visibilityState: 'hidden' },
    questionsRequest: 0, questions: [], pins: [], syncStatus: {}, issues: [], issueHistory: [], showIssues: false, loadError: '', reconcileRows,
    members: [{ id: 'old', name: 'old' }], choices: ['@old'],
    api: {
      rooms: async () => { calls.push('rooms'); return [{ id: 'a' }] },
      members: async () => { calls.push('members'); return [{ id: 'new', name: 'new' }] },
      mentions: async () => { calls.push('mentions'); return ['@new'] },
      questions: async () => { calls.push('questions'); return { a: [{ id: 'q-new' }] } },
      pins: async () => { calls.push('pins'); return [{ id: 'p-new' }] },
      syncStatus: async () => { calls.push('sync'); return { '/r': { origin: { ahead: 0, behind: 2 } } } },
      issues: async () => { calls.push('issues'); return [{ id: 'i-new' }] },
    },
  }
  const visible = (name) => c.pageVisible && listVisible(name, c)
  c.reload = createReloadGate(() => ({ room: c.roomId, visit: c.roomRequest }), visible)
  c.roomData = createRoomData(c.api, {
    current: () => ({ room: c.roomId, visit: c.roomRequest }), visible,
    members: (value) => { c.members = value },
    choices: (value) => { c.choices = value }, rooms() {}, error() {},
  })
  appFunctions(c, ['loadRooms', 'loadMembers', 'loadQuestions', 'loadPins', 'loadSync', 'loadIssues'])
  if (functions.some((n) => n.type === 'FunctionDeclaration' && n.id.name === 'refreshVisibleRoom')) appFunctions(c, ['refreshVisibleRoom'])
  const mounted = functions.find((n) => n.type === 'ExpressionStatement' && n.expression.callee?.name === 'onMount')
  const binding = mounted.expression.arguments[0].body.body.find((n) => n.type === 'VariableDeclaration' && n.declarations.some((d) => d.id.name === 'visibility'))
  const callback = binding.declarations.find((d) => d.id.name === 'visibility').init
  c.restoreVisibility = vm.runInContext(stripTypes(source.slice(callback.start, callback.end)), c)
  await Promise.all([c.loadMembers(), c.loadQuestions(), c.loadPins(), c.loadSync(), c.loadIssues()])
  assert.deepEqual(calls, [])
  c.document.visibilityState = 'visible'
  c.restoreVisibility()
  await new Promise(setImmediate)
  assert.deepEqual(c.members.map((m) => m.id), ['new'])
  assert.deepEqual(c.choices, ['@new'])
  assert.equal(c.questions[0]?.id, 'q-new')
  assert.equal(c.pins[0]?.id, 'p-new')
  assert.equal(c.syncStatus['/r'].origin.behind, 2)
  assert.equal(c.issues[0]?.id, 'i-new')
  assert.deepEqual(calls.sort(), ['issues', 'issues', 'members', 'mentions', 'pins', 'questions', 'rooms', 'sync'])
  c.roomData.destroy()
})

test('a channel roster change refreshes computers once and its later computer event updates the visible list', async () => {
  const one = { id: 'one', name: 'one' }, two = { id: 'two', name: 'two' }
  let computerRows = [{ session: 'one', name: 'one', state: 'none' }], calls = 0
  const c = {
    roomId: 'a', roomRequest: 1, view: 'channel:general', drill: null, memberChips: true,
    hierarchy: [one], members: [one], showClosed: false, pickedSessions: [],
    channels: { resolveView: (value) => value }, setPicked() {}, pruneSelection: () => [],
    soonRooms() {}, computerRequest: 0, computers: [...computerRows], computerFeed: {}, computerError: '',
    reconcileRows, applyEvent, openedApps, pushAction,
    api: { computers: async () => { calls++; return computerRows } },
  }
  c.reload = createReloadGate(() => ({ room: c.roomId, visit: c.roomRequest }), (name) => listVisible(name, c))
  appFunctions(c, ['memberRoster', 'roomCountKey', 'showMemberRows', 'parentKey', 'noteParents', 'rerouteLoaded', 'applyMembers', 'loadComputers', 'handleComputer'])
  computerRows = [...computerRows, { session: 'two', name: 'two', state: 'none' }]
  c.applyMembers([one, two])
  await new Promise(setImmediate)
  assert.equal(calls, 1)
  assert.equal(c.computers.some((row) => row.session === 'two'), true)
  c.handleComputer({ session: 'two', type: 'started', computer: { session: 'two', state: 'running', apps: [] } })
  assert.equal(c.computers.filter((row) => row.state === 'running').length, 1)
  c.applyMembers([{ ...one, state: 'live' }, two])
  await new Promise(setImmediate)
  assert.equal(calls, 1, 'status-only member changes must not reload computers')
  computerRows = [computerRows[0]]
  c.applyMembers([one])
  await new Promise(setImmediate)
  assert.equal(calls, 2)
  assert.equal(c.computers.some((row) => row.session === 'two'), false)
})

test('App session event bursts share transcript and queue requests through the reload gate', async () => {
  const waits = [deferred(), deferred()]
  let transcripts = 0, queues = 0, pokes = 0
  const c = {
    changesTracker: { poke: () => pokes++ },
    roomId: 'a', roomRequest: 1, drill: { id: 'one' }, home: null, feed: null,
    roomSessionController: null, openRun: '', soonRooms() {},
    loadMembers() {}, loadConsolidations() {},
  }
  c.sessionData = createSessionData({
    transcriptPage: () => waits[transcripts++].promise,
    sessionQueue: async () => { queues++; return [] },
  }, {
    current: () => ({ room: c.roomId, visit: c.roomRequest, id: c.drill?.id, selection: 1, turn: 0, limit: 6 }),
    transcript() {}, queue() {}, error() {},
  })
  appFunctions(c, ['loadSession', 'handle'])
  const initial = c.loadSession()
  for (let i = 0; i < 10; i++) c.handle({ kind: 'session', roomId: 'a', change: { op: 'upsert', id: 'one' } })
  assert.equal(transcripts, 1)
  assert.equal(queues, 1)
  assert.equal(pokes, 10, 'every event asks the throttled Changes count to refresh')
  waits[0].resolve({})
  await new Promise(setImmediate)
  assert.equal(transcripts, 2)
  assert.equal(queues, 2)
  waits[1].resolve({})
  await initial
  c.sessionData.destroy()
})

test('hidden pages skip session polling and discard late transcripts', async (t) => {
  t.mock.timers.enable({ apis: ['setInterval'] })
  let visible = true, calls = 0, applied = 0
  const wait = deferred()
  const c = { sessionData: createSessionData({
    transcriptPage: () => { calls++; return wait.promise }, sessionQueue: async () => [],
  }, {
    current: () => ({ room: 'a', visit: 1, id: 'one', selection: 1, live: true, visible }),
    transcript: () => applied++, queue() {}, error() {},
  }) }
  appFunctions(c, ['loadSession'])
  const pending = c.loadSession()
  visible = false
  c.sessionData.start()
  t.mock.timers.tick(3000)
  assert.equal(calls, 1)
  wait.resolve({})
  await pending
  assert.equal(applied, 0)
  c.sessionData.destroy()
})

test('App polling leaves visible and hidden control panels unchanged', async () => {
  const calls = []
  const c = {
    roomId: 'a', roomRequest: 1, view: 'github', drill: null, memberChips: false, pageVisible: true, needPair: false,
    bulkControllers: new Map(), setInterval: (fn) => fn,
    api: {
      sessionConsolidations: async () => { calls.push('consolidations'); return [] },
      roomSessionControls: async () => {
        calls.push('controls'); return { roomId: 'a', paused: false, running: 0, held: 0, dormant: 0 }
      },
    },
  }
  c.reload = createReloadGate(() => ({ room: c.roomId, visit: c.roomRequest }), (name) => listVisible(name, c))
  c.roomSessionController = createRoomSessionControls(c.api, 'a', () => {}, () => listVisible('roomControls', c))
  c.refreshHeldComments ??= () => {}
  vm.createContext(c)
  const mounted = functions.find((n) => n.type === 'ExpressionStatement' && n.expression.callee?.name === 'onMount')
  const binding = mounted.expression.arguments[0].body.body.find((n) => n.type === 'VariableDeclaration' && n.declarations.some((d) => d.id.name === 'transferPoll'))
  const callback = binding.declarations.find((d) => d.id.name === 'transferPoll').init.arguments[0]
  const poll = vm.runInContext(stripTypes(source.slice(callback.start, callback.end)), c)
  poll()
  await new Promise(setImmediate)
  assert.deepEqual(calls, [])
  c.memberChips = true
  poll()
  await new Promise(setImmediate)
  assert.deepEqual(calls, [])
  c.roomSessionController.destroy()
})

function sessionApp(tab, api) {
  let effect
  const c = {
    roomId: 'a', roomRequest: 1, drill: { id: 'one' }, sessionVisit: 1,
    sessionTurn: 0, sessionReadingHistory: false, mergeSessionPage, sessionTab: tab, pageVisible: true,
    session: null, sessionError: '', queue: [],
    untrack: (fn) => fn(), $effect: (fn) => { effect = fn },
  }
  const helpers = ['loadSession', 'showSession']
  if (functions.some((n) => n.type === 'FunctionDeclaration' && n.id.name === 'sessionVisible')) helpers.push('sessionVisible')
  appFunctions(c, helpers)
  const binding = functions.find((n) => n.type === 'VariableDeclaration' && n.declarations.some((d) => d.id.name === 'sessionData'))
  const hooks = binding.declarations.find((d) => d.id.name === 'sessionData').init.arguments[1]
  c.sessionData = createSessionData(api, vm.runInContext(stripTypes('(' + source.slice(hooks.start, hooks.end) + ')'), c))
  const entry = functions.find((n) => n.type === 'ExpressionStatement' && n.expression.callee?.name === '$effect' && source.slice(n.start, n.end).includes('const tab = sessionTab'))
  vm.runInContext(stripTypes(source.slice(entry.start, entry.end)), c)
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
  assert.equal(c.session?.turns[0]?.text, 'first message')
  reply = 'new reply'
  await c.loadSession()
  assert.equal(c.session.turns[0].text, 'new reply')
  c.sessionData.destroy()
})

test('returning to chat reloads a discarded first transcript without a room event', async () => {
  const first = deferred()
  let calls = 0
  const { c, effect } = sessionApp('chat', {
    transcriptPage: () => ++calls === 1 ? first.promise : Promise.resolve({ member: { id: 'one' }, turns: [] }),
    sessionQueue: async () => [],
  })
  const pending = c.loadSession()
  c.sessionTab = 'prs'
  effect()
  first.resolve({ member: { id: 'old' }, turns: [] })
  await pending
  assert.equal(c.session, null)
  c.sessionTab = 'chat'
  effect()
  await new Promise(setImmediate)
  assert.equal(calls, 2)
  assert.equal(c.session?.member?.id, 'one')
  c.sessionData.destroy()
})

test('live posts mark a missing newer page while the reader is in history', () => {
 const c={
  home:null,drill:null,roomId:'room',roomSessionController:null,feedAtBottom:false,
  feed:{room:{id:'room'},posts:[{id:'old'}]},soonRooms(){},loadVisuals(){},
 }
 appFunctions(c,['handle'])
 for(let i=0;i<1000;i++) c.handle({kind:'posts',roomId:'room',posts:[{id:'new'+i}]})
 assert.deepEqual(c.feed.posts.map(p=>p.id),['old'])
 assert.equal(c.feed.newer,1000)
})
