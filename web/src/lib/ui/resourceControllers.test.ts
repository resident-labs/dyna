import { test } from 'node:test'
import assert from 'node:assert/strict'
import { createRoomData } from '../rooms/roomData.ts'
import { createSessionData } from '../sessions/sessionData.ts'
import type { SessionApi, SessionHooks } from '../sessions/sessionData.ts'
import { createWorkflowData } from '../workflows/workflowData.ts'
import type { WorkflowApi, WorkflowHooks } from '../workflows/workflowData.ts'
import { need } from '../../../test/need.ts'
import { member } from '../../../test/session-fixtures.ts'
import type { Member } from '../api/room.ts'
import type { ListChange } from '../api/event.ts'

// A promise the test settles by hand.
function deferred<T = unknown>() {
  let resolve: (value: T) => void = () => {}
  let reject: (reason?: unknown) => void = () => {}
  const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no })
  return { promise, resolve, reject }
}

// The selection the controllers read. Each test sets the fields it needs.
interface Selection {
  room: string
  visit: number
  id: string
  turn?: number
  limit?: number
  visible?: boolean
  running?: boolean
  live?: boolean
  root?: boolean
  selection?: unknown
}

interface Update {
  name: string
  args: unknown[]
}

// The calls and hooks the controllers make. A test fills only the ones it
// drives; the rest stay absent.
interface FakeApi {
  members?: (...args: never[]) => unknown
  mentions?: (...args: never[]) => unknown
  openRoom?: (...args: never[]) => unknown
  rooms?: (...args: never[]) => unknown
  sessionQueue?: (...args: never[]) => unknown
  transcriptPage?: (...args: never[]) => unknown
  shellJobs?: (...args: never[]) => unknown
  runDetail?: (...args: never[]) => unknown
}

const hookNames = ['rooms', 'members', 'choices', 'opened', 'error', 'transcript', 'queue', 'jobs', 'patchJobs', 'jobsError', 'list', 'detail', 'listError', 'detailError'] as const
type HookName = (typeof hookNames)[number]

// patchMembers answers with the patched roster, so it keeps its real type.
type FakeHooks = {
  current: () => Selection
  members: (...args: never[]) => unknown
  choices: (...args: never[]) => unknown
  patchMembers?: (change: ListChange) => Member[]
} & Partial<Record<HookName, (...args: never[]) => unknown>>

// The fakes carry only what a test needs; the controllers read their full types.
const sessionData = (api: FakeApi, hooks: FakeHooks) => createSessionData(api as SessionApi, hooks as SessionHooks)
const workflowData = (api: FakeApi, hooks: FakeHooks) => createWorkflowData(api as WorkflowApi, hooks as WorkflowHooks)

function hooks() {
  let selection: Selection = { room: 'a', visit: 1, id: 'one', turn: 0, limit: 6, visible: true }
  const updates: Update[] = []
  const h: FakeHooks = { current: () => selection, members: () => {}, choices: () => {} }
  for (const name of hookNames) h[name] = (...args: never[]) => { updates.push({ name, args }) }
  return { h, updates, select: (value: Partial<Selection>) => { selection = { ...selection, ...value } } }
}

test('room member bursts use a trailing refresh and reject old visits', async () => {
  const s = hooks(), first = deferred(), second = deferred()
  let calls = 0
  const controller = createRoomData({ members: () => ++calls === 1 ? first.promise : second.promise, mentions: async () => [] }, s.h)
  const a = controller.loadMembers(), b = controller.loadMembers()
  assert.equal(calls, 1)
  first.resolve(['old'])
  await new Promise(setImmediate)
  assert.equal(calls, 2)
  second.resolve(['new']); await Promise.all([a, b])
  assert.deepEqual(s.updates.at(-1), { name: 'members', args: [['new']] })
  s.select({ visit: 2 })
  const pending = controller.loadMembers()
  s.select({ visit: 4 })
  await pending
  assert.equal(s.updates.filter((u) => u.name === 'members').length, 2)
  controller.destroy()
})

test('room opens before advisory member counts arrive', async () => {
  const s = hooks(), counts = deferred<Member[]>()
  const controller = createRoomData({ openRoom: async () => ({ members: [member('one')] }), members: () => counts.promise, mentions: async () => [] }, s.h)
  assert.equal(await controller.open(), true)
  assert.equal(s.updates.filter((u) => u.name === 'opened').length, 1)
  assert.equal(s.updates.filter((u) => u.name === 'members').length, 0)
  counts.resolve([member('one')])
  await new Promise(setImmediate)
  assert.equal(s.updates.filter((u) => u.name === 'members').length, 1)
  controller.destroy()
})

test('room open errors and room lists cannot update a destroyed app', async () => {
  const s = hooks(), opened = deferred(), rooms = deferred()
  const controller = createRoomData({ openRoom: () => opened.promise, rooms: () => rooms.promise }, s.h)
  const opening = controller.open(), listing = controller.loadRooms()
  controller.destroy()
  opened.reject(new Error('old error')); rooms.resolve(['old'])
  assert.equal(await opening, false)
  await listing
  assert.deepEqual(s.updates, [])
})

test('session transcript and queue share a trailing request for the same selection', async () => {
  const s = hooks(), old = deferred(), fresh = deferred(), oldQueue = deferred(), freshQueue = deferred()
  let calls = 0, queues = 0
  const controller = sessionData({
    transcriptPage: () => ++calls === 1 ? old.promise : fresh.promise,
    sessionQueue: () => ++queues === 1 ? oldQueue.promise : freshQueue.promise,
  }, s.h)
  const a = controller.load(), b = controller.load()
  assert.equal(calls, 1)
  old.resolve('old'); oldQueue.resolve(['old'])
  await new Promise(setImmediate)
  assert.equal(calls, 2)
  fresh.resolve('new'); freshQueue.resolve(['new']); await Promise.all([a, b])
  assert.deepEqual(Object.fromEntries(s.updates.map((u) => [u.name, u.args[0]])), { queue: ['new'], transcript: 'new' })
  controller.destroy()
})

test('session navigation and teardown reject late transcripts', async () => {
  const s = hooks(), transcript = deferred()
  const controller = sessionData({ transcriptPage: () => transcript.promise, sessionQueue: async () => [] }, s.h)
  const loading = controller.load()
  s.select({ id: 'two', visit: 3 })
  controller.destroy()
  transcript.resolve('old')
  await loading
  assert.deepEqual(s.updates, [])
})

test('shell job pages are passed through and late results from an old room visit are dropped', async () => {
  const s = hooks(), old = deferred()
  let calls = 0
  const controller = workflowData({ shellJobs: async () => ++calls === 1 ? old.promise : { items: [{ id: 'a', started: '1' }, { id: 'b', started: '2' }] } }, s.h)
  const stale = controller.loadJobs()
  s.select({ visit: 2 })
  const fresh = controller.loadJobs()
  old.reject(new Error('old job error')); await Promise.all([stale, fresh])
  assert.deepEqual(s.updates, [{ name: 'jobs', args: [{ items: [{ id: 'a', started: '1' }, { id: 'b', started: '2' }] }, false] }])
  controller.destroy()
})

test('workflow details and errors cannot replace a later selected run', async () => {
  const s = hooks(), old = deferred(), fresh = deferred()
  let calls = 0
  const controller = workflowData({ runDetail: () => ++calls === 1 ? old.promise : fresh.promise }, s.h)
  const a = controller.loadDetail()
  s.select({ id: 'two' })
  const b = controller.loadDetail()
  fresh.resolve('new'); await b
  old.reject(new Error('old error')); await a
  assert.deepEqual(s.updates, [{ name: 'detail', args: ['new'] }])
  controller.destroy()
})

test('feature controllers own and stop their polling and post timers', async (t) => {
  t.mock.timers.enable({ apis: ['setInterval', 'setTimeout'] })
  const s = hooks(), calls: string[] = []
  s.select({ live: true, running: true })
  const api: FakeApi = {
    sessionQueue: async () => [], transcriptPage: async () => { calls.push('transcript'); return {} },
    shellJobs: async () => { calls.push('jobs'); return [] }, runDetail: async () => { calls.push('run'); return {} },
  }
  const session = sessionData(api, s.h), workflow = workflowData(api, s.h)
  session.start(); workflow.start()
  session.schedule(); session.schedule()
  t.mock.timers.tick(150)
  await new Promise(setImmediate)
  assert.deepEqual(calls, ['transcript'])
  t.mock.timers.tick(1850)
  await new Promise(setImmediate)
  assert.deepEqual(calls, ['transcript', 'transcript', 'run'])
  session.schedule()
  session.destroy(); workflow.destroy()
  t.mock.timers.tick(10000)
  assert.equal(calls.length, 3)
})

test('shell jobs use events while only a selected running workflow polls', async (t) => {
  t.mock.timers.enable({ apis: ['setInterval'] })
  const s = hooks(), calls: string[] = []
  const workflow = workflowData({ shellJobs: async () => { calls.push('jobs'); return [] }, runDetail: async () => { calls.push('run'); return {} } }, s.h)
  workflow.start()
  t.mock.timers.tick(2000)
  await new Promise(setImmediate)
  assert.deepEqual<string[]>(calls, [])
  s.select({ running: true })
  t.mock.timers.tick(2000)
  await new Promise(setImmediate)
  assert.deepEqual(calls, ['run'])
  s.select({ visible: false })
  t.mock.timers.tick(4000)
  await new Promise(setImmediate)
  assert.deepEqual(calls, ['run'])
  workflow.destroy()
})


test('an initial transcript on a later visit rejects an older request for the same session', async () => {
  const s = hooks(), old = deferred()
  s.select({ selection: 1 })
  const controller = sessionData({ sessionQueue: async () => [], transcriptPage: () => old.promise }, s.h)
  const pending = controller.load()
  s.select({ selection: 3 })
  old.resolve('old')
  await pending
  assert.deepEqual(s.updates, [])
  controller.destroy()
})

test('slow polling does not start overlapping requests', async (t) => {
  t.mock.timers.enable({ apis: ['setInterval', 'setTimeout'] })
  const s = hooks(), pending = deferred(), calls: string[] = []
  s.select({ live: true, running: true })
  const api: FakeApi = {
    sessionQueue: async () => [], transcriptPage: async () => { calls.push('transcript'); return pending.promise },
    shellJobs: async () => [], runDetail: () => { calls.push('run'); return pending.promise },
  }
  const session = sessionData(api, s.h), workflow = workflowData(api, s.h)
  session.start(); workflow.start()
  t.mock.timers.tick(6000)
  assert.deepEqual(calls, ['transcript', 'run'])
  session.destroy(); workflow.destroy()
  pending.resolve({})
  await Promise.resolve()
})

test('room summaries load on home without a selected room', async () => {
  const s = hooks()
  s.select({ room: '' })
  const controller = createRoomData({ rooms: async () => [{ id: 'a' }] }, s.h)
  await controller.loadRooms()
  assert.deepEqual(s.updates, [{ name: 'rooms', args: [[{ id: 'a' }]] }])
  controller.destroy()
})


test('shell job patches wait for the current page without requesting another page', async () => {
  const s = hooks(), pending = deferred()
  let calls = 0
  const controller = workflowData({ shellJobs: () => { calls++; return pending.promise } }, s.h)
  const loading = controller.loadJobs()
  const change = { op: 'upsert', id: 'job', item: { id: 'job', state: 'exited' } }
  const patching = controller.patchJobs(change)
  pending.resolve({ items: [{ id: 'job', state: 'running' }] })
  await Promise.all([loading, patching])
  assert.equal(calls, 1)
  assert.deepEqual(s.updates.map((update) => update.name), ['jobs', 'patchJobs'])
  assert.equal(need(s.updates[1]).args[0], change)
  controller.destroy()
})


test('member patches wait for a roster request and update mentions without another request', async () => {
  const s = hooks(), pending = deferred()
  let memberCalls = 0, mentionCalls = 0
  const controller = createRoomData({ members: () => { memberCalls++; return pending.promise }, mentions: async () => { mentionCalls++; return ['@team', '@root', '@before'] } }, {
    ...s.h, patchMembers: () => [member('member', { name: 'after' }), member('closed', { name: 'closed', closed: true })],
  })
  const loading = controller.loadMembers()
  const patching = controller.patchMembers({ op: 'upsert', id: 'member', item: { id: 'member', name: 'after' } })
  pending.resolve([{ id: 'member', name: 'before' }])
  await Promise.all([loading, patching])
  assert.equal(memberCalls, 1)
  assert.equal(mentionCalls, 1)
  assert.deepEqual(need(s.updates.filter((update) => update.name === 'choices').at(-1)).args[0], ['@team', '@root', '@after'])
  controller.destroy()
})
test('DM and thread history share one request and wait behind a live refresh', async () => {
  const s = hooks(), tail = deferred(), old = deferred(), calls: unknown[] = []
  const controller = sessionData({
    transcriptPage: (id: string, cursor: string, limit: number) => { calls.push({ id, cursor, limit }); return cursor ? old.promise : tail.promise },
    sessionQueue: async () => [],
  }, s.h)
  const refresh = controller.load()
  const dm = controller.earlier('7'), thread = controller.earlier('7')
  assert.equal(dm, thread)
  assert.equal(calls.length, 1)
  tail.resolve({ turns: [{ n: 9 }] })
  await new Promise(setImmediate)
  assert.deepEqual(calls, [{ id: 'one', cursor: '', limit: 6 }, { id: 'one', cursor: '7', limit: 6 }])
  old.resolve({ turns: [{ n: 6 }] })
  assert.equal(await dm, true)
  await refresh
  assert.deepEqual(s.updates.at(-1), { name: 'transcript', args: [{ turns: [{ n: 6 }] }, true] })
  controller.destroy()
})

test('an older session page cannot enter a different session after navigation', async () => {
  const s = hooks(), old = deferred()
  const controller = sessionData({ transcriptPage: () => old.promise }, s.h)
  const loading = controller.earlier('7')
  s.select({ id: 'two', selection: 2 })
  old.resolve({ member: { id: 'one' }, turns: [{ n: 6 }] })
  assert.equal(await loading, false)
  assert.deepEqual(s.updates, [])
  controller.destroy()
})
