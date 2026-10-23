import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { isFinished } from '../rooms/memberOrder.ts'
import { selectableSession } from './consolidation.ts'
import { appLoaderStubs } from '../rooms/appStubs.ts'
import { reconcileRows } from '../ui/reloadGate.ts'
import { mergeFeedPage, rerouteAnchors, withViews } from '../rooms/feedPages.ts'
import { call, runFunctions } from '../../../test/svelte-source.ts'
import { installRouterShim } from '../../../test/appRouterShim.ts'
import { installRoomLoader } from '../../../test/appRoomLoader.ts'
import { isRecord } from '../api/guards.ts'
import { need } from '../../../test/need.ts'

const source = readFileSync(new URL('../../App.svelte', import.meta.url), 'utf8')
const names = ['reopenSession', 'dismissSession', 'menuItems']
// The slice of App's state these handlers read and write.
interface Member {
  id: string
  name?: string
  closed?: boolean
  resumable?: boolean
  state?: string
  status?: string
  role?: string
  parent?: string
  finished?: boolean
  views?: string[]
}
interface App {
  api: Record<string, (...args: never[]) => unknown>
  reopening: string
  dismissing: string
  roomRequest: number
  sessionVisit: number
  drill: Member
  session: { member: Member; turns: number[] } | null
  transcripts?: Record<string, unknown>
  members: Member[]
  feed: { posts: Member[]; newer?: number } | null
  [name: string]: unknown
}
const closed: Member = { id: 'one', name: 'worker', closed: true, resumable: true }
function app(reopen: (room: string, id: string) => unknown) {
  const calls: string[] = []
  const c: App = { loadComputers() {}, soonRooms() {}, expediteItems: () => [], hierarchy: [], reconcileRows, isFinished, api: { reopenSession: reopen }, reopening: '', dismissing: '', roomId: 'a', roomRequest: 1, sessionVisit: 1, drill: closed, session: { member: closed, turns: [1] }, members: [], feed: null, loadSession: async () => calls.push('transcript'), loadMembers: async () => calls.push('members'), say: (text: string) => calls.push(text) }
  // The open transcript lives in lib/app/sessionView.svelte.ts now.
  Reflect.set(c, 'transcripts', {
    get visit() { return Number(c.sessionVisit) },
    nextVisit() { c.sessionVisit = Number(c.sessionVisit) + 1 },
    patchMember(member: Member) { if (c.session && c.session.member?.id === member.id) c.session = { ...c.session, member } },
    openFresh() { c.session = null }, clear() { c.session = null }, setSession(value: unknown) { c.session = value as App['session'] },
  })
  c.routedParents ??= ''; c.selectableSession = selectableSession
  // The screen state lives in the router now, so the sliced functions write through it.
  installRouterShim(c)
  runFunctions(source, names, c)
  // The member rows and the opened room's feed live in the room loader now.
  installRoomLoader(c, c.api)
  return { c, calls }
}
function deferred<T = never>() { let resolve: (value: T) => void = () => {}, reject: (error: Error) => void = () => {}; const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no }); return { promise, resolve, reject } }
// The labels of the menu items for a member.
function menu(c: App, member: Member): string[] {
  const items = call(c, 'menuItems', member)
  assert.ok(Array.isArray(items))
  return items.flatMap((item: unknown) => (isRecord(item) && typeof item.label === 'string' ? [item.label] : []))
}

test('reopen updates the transcript and refreshes members without starting a turn', async () => {
  const member = { ...closed, closed: false, state: 'idle' }
  const { c, calls } = app(async (room, id) => { assert.equal(room, 'a'); assert.equal(id, 'one'); return member })
  await call(c, 'reopenSession', closed)
  assert.equal(c.session?.member, member)
  assert.equal(c.drill, member)
  assert.deepEqual([...c.session.turns], [1])
  assert.deepEqual(calls.slice(0, 2), ['transcript', 'members'])
  assert.equal(c.reopening, '')
})

test('duplicate clicks share one pending action and refusal remains retryable', async () => {
  const pending = deferred<Member>(); let requests = 0
  const { c, calls } = app(() => { requests++; return pending.promise })
  const first = call(c, 'reopenSession', closed)
  await call(c, 'reopenSession', closed)
  assert.equal(requests, 1)
  pending.reject(new Error('The name is already in use.'))
  await assert.rejects(first as Promise<unknown>, /name is already in use/)
  assert.equal(c.reopening, '')
  assert.equal(c.session?.member.closed, true)
  assert.equal(calls.length, 0)
})

test('late reopen cannot change a different room visit or transcript selection', async () => {
  for (const changed of ['roomRequest', 'sessionVisit'] as const) {
    const pending = deferred<Member>()
    const { c, calls } = app(() => pending.promise)
    const first = call(c, 'reopenSession', closed)
    c[changed] += 2
    pending.resolve({ ...closed, closed: false })
    await first
    assert.equal(c.session?.member.closed, true)
    assert.equal(calls.includes('transcript'), false)
    assert.equal(calls.includes('members'), changed === 'sessionVisit')
  }
})

test('only closed resumable sessions offer Reopen', async () => {
  const { c } = app(async () => ({}))
  for (const member of [closed, { ...closed, resumable: false }, { ...closed, closed: false }]) {
    assert.equal(menu(c, member).includes('Reopen'), !!member.closed && !!member.resumable)
  }
})

test('finished sessions stay visible as open members and offer Dismiss', async () => {
  const finished: Member = { id: 'one', name: 'worker', resumable: true, state: 'idle', status: 'dormant' }
  const { c, calls } = app(async () => ({}))
  Object.assign(c, { showClosed: false, setPicked() {}, pruneSelection: () => [], pickedSessions: [], feedViews: { resolveView: (view: string) => view }, view: 'general' })
  call(c, 'applyMembers', [finished, { ...closed, id: 'gone' }, { id: 'open', closed: false }], [])
  assert.deepEqual([...c.members].map((m) => m.id), ['one', 'open'])
  assert.equal(menu(c, finished).includes('Dismiss'), true)
  assert.equal(menu(c, finished).includes('Reopen'), false)
  assert.equal(menu(c, closed).includes('Dismiss'), false)
  if (c.session) c.session.member = finished
  c.api.dismissSession = async (room: string, id: string) => { assert.equal(room, 'a'); assert.equal(id, 'one'); return closed }
  await call(c, 'dismissSession', finished)
  assert.equal(c.session?.member.finished, undefined)
  assert.deepEqual(calls.slice(0, 2), ['transcript', 'members'])
  assert.equal(c.dismissing, '')
})

test('dismiss refuses duplicate actions and keeps errors retryable', async () => {
  const pending = deferred<Member>(); let requests = 0
  const { c } = app(async () => ({}))
  c.api.dismissSession = () => { requests++; return pending.promise }
  const finished = { ...closed, closed: false, status: 'dormant' }
  const first = call(c, 'dismissSession', finished)
  await call(c, 'dismissSession', finished)
  assert.equal(requests, 1)
  pending.reject(new Error('Could not save the session.'))
  await assert.rejects(first as Promise<unknown>, /Could not save/)
  assert.equal(c.dismissing, '')
})

test('the internal root offers Transfer but not Consolidate or Promote', async () => {
  const { c } = app(async () => ({}))
  const labels = menu(c, { id: 'root', name: 'root', role: 'root', state: 'idle' })
  assert.equal(labels.includes('Consolidate sessions…'), false)
  assert.equal(labels.includes('Promote to lead'), false)
  assert.equal(labels.includes('Transfer to new model…'), true)
  assert.equal(menu(c, { id: 'one', name: 'worker', state: 'idle' }).includes('Consolidate sessions…'), true)
})

test('a promote right after opening a room rereads the views of a history window', async () => {
  const { c } = app(async () => ({}))
  const lead = { id: 'lead', name: 'lead' }, child = { id: 'child', name: 'child', parent: 'lead' }
  const old = { id: 'old', views: ['channel:lead'] }
  const arounds: unknown[][] = []
  Object.assign(c, { showClosed: false, setPicked() {}, pruneSelection: () => [], pickedSessions: [], feedViews: { resolveView: (view: string) => view }, view: 'channel:lead', feed: null, feedAtBottom: false,
    workflowData: { loadJobs() {} }, ...appLoaderStubs(), mergeFeedPage, rerouteAnchors, withViews })
  c.api.feedAround = async (room: string, id: string, keep: number) => { arounds.push([room, id, keep]); return { posts: [{ id: 'old', views: ['general'] }] } }
  // A history window: newer posts are missing, so a tail read would not reach it.
  call(c, 'applyOpened', { feed: { room: { id: 'a' }, posts: [old], newer: 40 }, members: [lead, child] })
  call(c, 'applyMembers', [lead, { ...child, parent: '' }])
  await new Promise((resolve) => setImmediate(resolve))
  assert.deepEqual(arounds, [['a', 'old', 200]])
  assert.deepEqual(need(c.feed).posts[0]?.views, ['general'])
  assert.equal(need(c.feed).newer, 40, 'the window stays where the owner is reading')
})
