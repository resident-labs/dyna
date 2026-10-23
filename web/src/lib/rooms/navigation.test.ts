import { test } from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import { setImmediate } from 'node:timers/promises'
import { parse } from 'svelte/compiler'
import { GENERAL, VOICE, isFeedView, inView, homeView, feedModel } from './feedViews.ts'
import type { FeedMember } from './feedViews.ts'
import { appLoaderStubs } from './appStubs.ts'
import { reconcileRows } from '../ui/reloadGate.ts'
import { createRoomData } from './roomData.ts'
import { pruneSelection } from '../sessions/consolidation.ts'
import { detailHref, parseDetailHref } from '../ui/details.ts'
import { callTarget } from '../voice/callRequests.ts'
import { prHash, parsePRHash, issueHash, parseIssueHash, parseFileHash } from '../prs/prlink.ts'
import { roomIDPattern as ROOM, postIDPattern as POST, postHref } from '../ui/posts.ts'
import { questionID } from '../questions/questions.ts'
import { isShellResult, shellJobID } from './feedRows.ts'
import { trimFeed } from './feedWindow.ts'
import { mergeFeedPage, rerouteAnchors, withViews } from './feedPages.ts'
import { reloadsUnread } from '../ui/unreadReload.ts'
import { ROOT_SESSION, parseRoute, routeHash, routeSession, writeRoute } from '../ui/route.ts'
import { sourceRange, stripTypes, moduleFunctions } from '../../../test/svelte-source.ts'
import { installRoomLoader } from '../../../test/appRoomLoader.ts'
import { goalsCode, installGoals, installIssues, installMachines, installPins, installPRs, installQuestions, installComputers, installSelection, installSessionView, installSync, machinesCode } from '../../../test/appModules.ts'

// Run the actual App navigation functions with controlled API responses.
// The Svelte parser selects complete declarations without copying their logic.
const source = readFileSync(new URL('../../App.svelte', import.meta.url), 'utf8')
// The screen's own navigation moved to lib/app/router.svelte.ts; the room,
// detail and selection functions still live in App.
const routerSource = readFileSync(new URL('../app/router.svelte.ts', import.meta.url), 'utf8')
// The open room's data moved to lib/app/roomLoader.svelte.ts.
const loaderSource = readFileSync(new URL('../app/roomLoader.svelte.ts', import.meta.url), 'utf8')
const loaderCode = moduleFunctions(loaderSource)
const declarations = parse(source, { modern: true }).instance?.content.body ?? []
// The member selection moved to lib/app/selection.svelte.ts with the reset that
// selectRoom runs, so installSelection brings it into the state.
const names = ['openReview', 'clearItemLink', 'holdsItemLink', 'syncRoute', 'routeHere', 'hashChanged', 'selectRoom', 'openTarget', 'hashTarget', 'openDetail', 'openView', 'jumpTo', 'showRun', 'openPR']
const routerCode = moduleFunctions(routerSource, names)
const appCode = (name: string): string | undefined => {
  const node = declarations.find((entry) => entry.type === 'FunctionDeclaration' && entry.id?.name === name)
  if (!node) return undefined
  const { start, end } = sourceRange(node)
  return source.slice(start, end)
}
const picked = names.flatMap((name) => appCode(name) ?? [])
assert.equal(picked.length + routerCode.size, names.length)
const code = [...picked, ...routerCode.values()].join('\n')
// The tests add the functions they call by slicing the same declarations.
function runDeclaration(target: object, name: string): void {
  const text = appCode(name) ?? routerCode.get(name) ?? loaderCode.get(name) ?? goalsCode.get(name) ?? machinesCode.get(name)
  if (text === undefined) {
    assert.equal(typeof Reflect.get(target, name), 'function', name)
    return
  }
  vm.runInContext(stripTypes(text), target)
}

const A = 'room_20261017-100000-aaaaaaaa'
const B = 'room_20261017-100000-bbbbbbbb'

interface NavTarget {
  kind: string
  id: string
  turn: number
  roomId: string
}

interface NavPost {
  id: string
  from?: string
  to?: string[]
  views?: string[]
  unread?: boolean
  completion?: boolean
  kind?: string
}

interface NavFeed {
  room: { id: string }
  posts: NavPost[]
  earlier?: number
  cursor?: string
  newer?: number
  offset?: number
  viewUnread?: Record<string, number>
}

interface NavGoal {
  id: string
  state?: string
  text?: string
  holders?: string[]
}

interface NavDrill {
  id?: string
  name?: string
}

interface PrLink {
  roomId?: string
  pr?: string
  issue?: number
  tab?: string
}

interface NavMember {
  id?: string
  name?: string
  address?: string
  owner?: boolean
  parent?: string
  rootSeat?: boolean
}

interface NavApi {
  openRoom: (id: string) => Promise<unknown>
  rooms: () => Promise<unknown[]>
  members: (room: string, all?: boolean) => Promise<unknown[]>
  mentions: (room: string) => Promise<unknown[]>
  feedTail: (room: string, cursor: string, limit: number) => Promise<unknown>
  feedAround: (room: string, target: string, limit: number) => Promise<unknown>
  transcript: (id: string) => Promise<{ member: NavMember }>
  transcriptTurn: (id: string, turn: number) => Promise<{ member: NavMember }>
  goal: (room: string, id: string) => Promise<NavGoal>
  [name: string]: unknown
}

interface NavState {
  api: NavApi
  roomData?: { trimAtView(next: string, previous: string): void }
  roomRefreshes: Set<unknown>
  roomTimer: (() => void) | null
  roomId: string
  roomRequest: number
  questionsRequest: number
  detailRequest: number
  reviewOpen: boolean
  reviewTab: string
  reviewVersion: number
  mergeVersion: number
  narrow: boolean
  hasTerminal: boolean
  selectionRevision: number
  consolidationView: number
  consolidationAlive: boolean
  memberRequest: number
  selectingSessions: boolean
  pickedSessions: string[]
  consolidating: { roomId: string; sessions: string[] } | null
  showClosed: boolean
  needPair: boolean
  rooms: { id: string }[]
  posts: NavPost[]
  view: string
  openRun: string
  openJob: string
  drill: NavDrill | null
  card?: { id?: string } | null
  viewing?: { id?: string; postId?: string } | null
  feed: NavFeed | null
  feedAtBottom: boolean
  location: { hash: string; pathname?: string; search?: string }
  history?: { replaceState(state: unknown, title: string, url: string): void; pushState(state: unknown, title: string, url: string): void }
  started: boolean
  routing: number
  routeFix: boolean
  pop: unknown
  readOnlyDm: boolean
  sessionTurn: number
  entries: [string, string][]
  prLink: PrLink | null
  unreadFrom: string
  loadError: string
  goals: NavGoal[]
  goalId: string
  fetchedGoal: NavGoal | null
  fetchedGoalVersion: number
  hierarchy: (FeedMember & { address?: string })[]
  feedViews: ReturnType<typeof feedModel>
  earlierLoad: unknown
  STEP: number
  workflowData: { loadJobs(): void } | null
  roomSessionController: unknown
  home: unknown
  feedView: { jump(): void }
  applyItemEvent: (change: unknown) => boolean
  applyUnread: (value: unknown) => void
  afterRead: () => void
  handle: (event: unknown) => void
  loadEarlier: (...args: unknown[]) => unknown
  loadRooms: () => void
  loadMembers: () => void
  loadChanges: () => void
  loadGoals: () => void
  loadRules: () => void
  loadVisuals: () => void
  loadReviewer: () => void
  loadRuns: () => void
  loadRun: () => void
  loadConsolidations: () => void
  loadQuestions: () => void
  visitRoomControls: () => void
  markShown: (posts: NavPost[]) => void
  say: () => void
  openSession: (member: NavMember) => void
  applyRoomOpened: (value: unknown) => void
  applyOpened: (value: unknown) => void
  reconcileRows: typeof reconcileRows
  mergeFeedPage: typeof mergeFeedPage
  ROOM: RegExp
  POST: RegExp
  GENERAL: string
  VOICE: string
  isFeedView: typeof isFeedView
  inView: typeof inView
  homeView: typeof homeView
  questionID: typeof questionID
  isShellResult: typeof isShellResult
  shellJobID: typeof shellJobID
  trimFeed: typeof trimFeed
  rerouteAnchors: typeof rerouteAnchors
  withViews: typeof withViews
  pruneSelection: typeof pruneSelection
  reloadsUnread: typeof reloadsUnread
  ROOT_SESSION: string
  parseRoute: typeof parseRoute
  routeHash: typeof routeHash
  routeSession: typeof routeSession
  writeRoute: typeof writeRoute
  parseDetailHref: typeof parseDetailHref
  prHash: typeof prHash
  parsePRHash: typeof parsePRHash
  parseIssueHash: typeof parseIssueHash
  parseFileHash: typeof parseFileHash
  callTarget: typeof callTarget
  callPush: unknown
  URLSearchParams: typeof URLSearchParams
  tick: () => Promise<void>
  routedParents: string
}

interface NavFns {
  openReview: (...args: unknown[]) => unknown
  clearItemLink: () => void
  holdsItemLink: () => boolean
  syncRoute: () => void
  routeHere: () => boolean
  hashChanged: () => void
  selectRoom: (room: string, workspace?: string) => Promise<boolean>
  openTarget: (...args: unknown[]) => Promise<boolean>
  hashTarget: () => boolean
  openDetail: (target: NavTarget) => Promise<unknown>
  openView: (view: string) => void
  jumpTo: (id: string) => Promise<unknown>
  showRun: (run: { id: string }) => void
  openPR: (number: number) => void
  resync: () => Promise<void>
  goalById: (id: string) => NavGoal
  openGoal: (id: string) => Promise<NavGoal>
  trackFetchedGoal: (change: { op: string; id: string; item?: NavGoal }) => void
  refreshFetchedGoal: () => Promise<void>
  roomSessionItems: (...args: unknown[]) => unknown
}

interface NavApp extends NavState, NavFns {}

interface Deferred<T> {
  promise: Promise<T>
  resolve: (value: T) => void
}
function deferred<T = unknown>(): Deferred<T> {
  let resolve: (value: T) => void = () => { throw new Error('the deferred value is not ready') }
  const promise = new Promise<T>((r) => { resolve = r })
  return { promise, resolve }
}

const target = (kind: string): NavTarget => ({ kind, id: `${{ shell: 'sh', run: 'wf', session: 'ses' }[kind]}_20261017-100000-aaaaaaaa`, turn: kind === 'session' ? 3 : 0, roomId: A })
const opened = (id: string) => ({ feed: { room: { id }, posts: [] } })

function navigation() {
  const pending = deferred()
  const calls: string[] = []
  const applied: string[] = []
  const state: NavState = {
    reviewOpen: false, reviewTab: 'files', reviewVersion: 0, mergeVersion: 0, roomId: B, roomRequest: 0, questionsRequest: 0, detailRequest: 0, narrow: false, hasTerminal: true,
    selectionRevision: 0, consolidationView: 0, consolidationAlive: true, memberRequest: 0, pickedSessions: [], pruneSelection,
    showClosed: false, rooms: [{ id: A }, { id: B }], needPair: false,
    ROOM, POST, GENERAL, VOICE, routedParents: '', isFeedView, inView, homeView, isShellResult, shellJobID, trimFeed, mergeFeedPage, questionID, feedViews: feedModel([]), posts: [],
    view: GENERAL, openRun: '', openJob: '', drill: null, feed: null, feedAtBottom: true, location: { hash: '', pathname: '/', search: '' },
    started: true, routing: 0, routeFix: false, pop: null, readOnlyDm: false, sessionTurn: 0, entries: [], prLink: null, unreadFrom: '', loadError: '',
    goals: [], goalId: '', fetchedGoal: null, fetchedGoalVersion: 0, hierarchy: [], earlierLoad: null, STEP: 50, workflowData: null, roomSessionController: null, home: null,
    selectingSessions: false, consolidating: null, roomRefreshes: new Set<unknown>(), roomTimer: null,
    ROOT_SESSION, parseRoute, routeHash, routeSession, writeRoute, parseDetailHref, prHash, parsePRHash, parseIssueHash, parseFileHash, callTarget, callPush: null, URLSearchParams, tick: () => Promise.resolve(),
    loadRooms() {}, visitRoomControls() {}, loadConsolidations() {}, markShown() {}, loadEarlier() { return false }, say() {},
    loadChanges() {}, loadReviewer() {}, loadRuns() {}, loadRun() { calls.push('run') },
    loadGoals() {}, loadRules() {}, loadVisuals() {}, loadQuestions() {}, loadMembers() {},
    feedView: { jump() { calls.push('post') } },
    openSession() { calls.push('session') },
    applyRoomOpened(o: unknown) { applied.push((o as { feed: NavFeed }).feed.room.id) },
    applyItemEvent: () => false, applyUnread: () => {}, applyOpened: () => {}, afterRead: () => {}, handle: () => {},
    reconcileRows, rerouteAnchors, withViews, reloadsUnread,
    api: {
      openRoom(id: string) { return id === A ? pending.promise : Promise.resolve(opened(id)) },
      rooms: async () => [],
      members: async () => [],
      mentions: async () => [],
      feedTail: async () => ({}),
      feedAround: async () => ({}),
      transcriptTurn() { calls.push('transcript'); return Promise.resolve({ member: {} }) },
      transcript() { calls.push('transcript'); return Promise.resolve({ member: {} }) },
      goal: async () => ({ id: '' }),
      goalItems: async () => [],
      changes: async () => ({ items: [], older: false, cursor: '' }),
      reviewerStatus: async () => null,
      pins: async () => [],
      questions: async () => ({}),
      visuals: async () => [],
      issues: async () => [],
      syncStatus: async () => ({}),
    },
  }
  // The room data the sliced functions write lives in the loader now.
  installRoomLoader(state, state.api)
  // The machine list, the room's goals and the toast line live in lib/app
  // modules now; their functions run here over the same state.
  installMachines(state)
  installGoals(state)
  installQuestions(state)
  installPins(state)
  installIssues(state)
  installSync(state)
  installSessionView(state)
  installPRs(state)
  installSelection(state)
  installComputers(state)
  // The router asks the page for the side of a screen change it does not own.
  Reflect.set(state, 'host', {
    started: () => state.started,
    pop: () => !!state.pop,
    followHash: () => (Reflect.get(state, 'hashTarget') as () => boolean)(),
    leaveRoom: () => { void (Reflect.get(state, 'selectRoom') as (id: string) => Promise<boolean>)('') },
    closePanels: () => {},
    closeDrawer: () => {},
    resolveView: (v: string) => (state.feedViews as { resolveView: (view: string) => string }).resolveView(v),
    trimFeedAtView: (next: string, previous: string) => {
      (state.roomData as { trimAtView(n: string, p: string): void }).trimAtView(next, previous)
    },
    leaveScreen: () => {},
    clearRunDetail: () => { Reflect.set(state, 'runDetail', null) },
    loadChanges: () => (state.loadChanges as () => void)(),
    loadReviewer: () => (state.loadReviewer as () => void)(),
    loadRuns: () => (state.loadRuns as () => void)(),
    loadRun: () => (state.loadRun as () => void)(),
  })
  // entries records each address write: ['push' | 'replace', hash].
  const write = (kind: string, url: string) => { state.location.hash = url.includes('#') ? url.slice(url.indexOf('#')) : ''; state.entries.push([kind, state.location.hash]) }
  state.history = { replaceState(_state: unknown, _title: string, url: string) { write('replace', url) }, pushState(_state: unknown, _title: string, url: string) { write('push', url) } }
  vm.createContext(state)
  vm.runInContext(stripTypes(code), state)
  const fns = vm.runInContext(`({ ${names.join(', ')} })`, state) as Pick<NavApp, keyof NavFns>
  return { c: Object.assign(state, fns) as NavApp, pending, calls, applied }
}

for (const kind of ['shell', 'run', 'session']) {
  for (const later of ['room', 'view']) {
    test(`a delayed cross-room ${kind} link preserves a later ${later} choice`, async () => {
      const { c, pending, calls, applied } = navigation()
      const opening = c.openDetail(target(kind))
      if (later === 'room') await c.selectRoom(B)
      else c.openView('files')
      pending.resolve(opened(A))
      await opening
      assert.equal(c.roomId, later === 'room' ? B : A)
      assert.equal(c.view, GENERAL)
      assert.equal(c.openJob, '')
      assert.deepEqual(calls, [])
      assert.deepEqual(applied, [later === 'room' ? B : A])
    })
  }
}

for (const later of ['room', 'view']) {
  test(`a copied cross-room link preserves a later ${later} choice`, async () => {
    const { c, pending, calls } = navigation()
    c.location.hash = detailHref(target('session'), A)
    assert.equal(c.hashTarget(), true)
    if (later === 'room') await c.selectRoom(B)
    else c.openView('files')
    pending.resolve(opened(A))
    await setImmediate()
    assert.equal(c.roomId, later === 'room' ? B : A)
    assert.equal(c.view, GENERAL)
    assert.equal(c.openJob, '')
    assert.deepEqual(calls, [])
  })
}

test('the copied-hash continuation also stops after its room target has resolved', async () => {
  const { c, pending, calls } = navigation()
  const openTarget = c.openTarget
  c.openTarget = async (...args: unknown[]) => {
    const selected = await openTarget(...args)
    queueMicrotask(() => c.openView('files'))
    return selected
  }
  c.location.hash = detailHref(target('shell'), A)
  c.hashTarget()
  pending.resolve(opened(A))
  await setImmediate()
  assert.equal(c.view, GENERAL)
  assert.equal(c.reviewTab, 'files')
  assert.equal(c.openJob, '')
  assert.deepEqual(calls, [])
})

test('a PR link opens the Pull requests view on that PR and stays in the address bar', async () => {
  const { c, pending } = navigation()
  c.location.hash = prHash(A, 741, 'checks')
  assert.equal(c.hashTarget(), true)
  pending.resolve(opened(A))
  await setImmediate()
  assert.equal(c.roomId, A)
  assert.equal(c.view, GENERAL)
  assert.equal(c.reviewTab, 'prs')
  assert.deepEqual({ ...c.prLink }, { roomId: A, pr: '741', tab: 'checks' })
  assert.equal(c.location.hash, prHash(A, 741, 'checks'))
  // A later view choice drops the link, so the PR screen does not reopen it.
  c.openView('files')
  assert.equal(c.prLink, null)
})

test('Back and Forward between PR links stay with the open Pull requests view', () => {
  const { c } = navigation()
  c.view = GENERAL; c.reviewOpen = true; c.reviewTab = 'prs'
  let opened = false
  c.openTarget = () => { opened = true; return Promise.resolve(false) }
  c.location.hash = prHash(B, 742)
  assert.equal(c.hashTarget(), true)
  assert.equal(opened, false)
  assert.equal(c.location.hash, prHash(B, 742))
})

test('a current cross-room link still opens the requested log', async () => {
  const { c, pending } = navigation()
  const opening = c.openDetail(target('shell'))
  pending.resolve(opened(A))
  await opening
  assert.equal(c.roomId, A)
  assert.equal(c.view, 'jobs')
  assert.equal(c.openJob, target('shell').id)
})

test('a current copied link still opens its exact transcript', async () => {
  const { c, pending, calls } = navigation()
  c.location.hash = detailHref(target('session'), A)
  c.hashTarget()
  pending.resolve(opened(A))
  await setImmediate()
  assert.equal(c.roomId, A)
  assert.deepEqual(calls, ['transcript', 'session'])
})

test('opening a room or a feed view does not load the whole history', async () => {
  const { c } = navigation()
  let loads = 0
  c.loadEarlier = () => { loads++ }
  const lead = { id: 'lead', name: 'lead', address: 'repo:lead' }
  c.feedViews = feedModel([lead])
  assert.equal(await c.selectRoom(B), true)
  c.openView('channel:lead')
  assert.equal(loads, 0)
})

test('a later same-room selection wins over an older room response', async () => {
  const { c, pending, applied } = navigation()
  const first = c.selectRoom(A)
  c.api.openRoom = () => Promise.resolve(opened(A))
  assert.equal(await c.selectRoom(A, 'files'), true)
  pending.resolve(opened(A))
  assert.equal(await first, false)
  assert.equal(c.view, GENERAL)
  assert.equal(c.reviewTab, 'files')
  assert.deepEqual(applied, [A])
})

for (const workspace of ['files', 'github', 'jobs']) {
  test(`opening saved ${workspace} does not mark hidden feed view messages read`, async () => {
    const { c } = navigation()
    c.posts = [{ id: 'hidden-child-result', unread: true }, { id: 'other-channel-result', unread: true }]
    const marked: NavPost[] = []
    c.markShown = (posts) => { marked.push(...posts) }
    assert.equal(await c.selectRoom(B, workspace), true)
    assert.equal(c.view, workspace === 'jobs' ? 'jobs' : GENERAL)
    assert.deepEqual(marked, [])
  })
}

test('a pin 10,000 posts back waits for the in-flight page then uses one around request', async () => {
  const { c, calls } = navigation()
  const lead = { id: 'lead', name: 'lead', address: 'repo:lead' }
  const child = { id: 'child', name: 'child', address: 'repo:child', parent: 'lead' }
  c.feedViews = feedModel([lead, child])
  c.feed = { room: { id: B }, posts: [], earlier: 10000, cursor: 'first' }
  c.earlierLoad = null
  c.STEP = 100
  const history = deferred()
  const result = { id: 'old-result', from: child.address, completion: true, views: ['channel:lead'] }
  const pages: unknown[][] = []
  c.api.feedTail = (id, before, limit) => {
    pages.push([id, before, limit])
    return pages.length === 1 ? history.promise : Promise.resolve({ room: { id: B }, posts: [result], earlier: 0 })
  }
  c.api.feedAround = (id, target, limit) => {
    pages.push([id, target, limit])
    return Promise.resolve({ room: { id: B }, posts: [result], earlier: 1, newer: 9999 })
  }
  runDeclaration(c, 'loadEarlier')
  const loading = c.loadEarlier()
  const jumping = c.jumpTo(result.id)
  await setImmediate()
  assert.deepEqual(calls, [])
  history.resolve({ room: { id: B }, posts: [], earlier: 100, cursor: 'second' })
  await Promise.all([loading, jumping])
  assert.equal(c.view, 'channel:lead')
  assert.deepEqual(calls, ['post'])
  assert.deepEqual(pages, [[B, 'first', 100], [B, result.id, 100]])
})

test('a pin to a shell result opens its job in shell-jobs', async () => {
  const { c, calls } = navigation()
  const shell = 'sh_20261017-120000-0000000a'
  c.feed = { room: { id: B }, posts: [{ id: 'shell-result', from: `shell:${shell}`, kind: 'failed', completion: true }] }
  await c.jumpTo('shell-result')
  assert.equal(c.view, 'jobs')
  assert.equal(c.openJob, shell)
  assert.deepEqual(calls, [])
})

test('a pin waiting for history preserves a later workspace selection', async () => {
  const { c, calls } = navigation()
  const history = deferred()
  c.feed = { room: { id: B }, posts: [], earlier: 1, cursor: 'first' }
  c.loadEarlier = async () => { await history.promise; return false }
  const jumping = c.jumpTo('old-result')
  c.openView('files')
  history.resolve(undefined)
  await jumping
  assert.equal(c.view, GENERAL)
  assert.equal(c.reviewTab, 'files')
  assert.deepEqual(calls, [])
})

test('reconnect moves a stale lead view to the room chat after its last child is promoted', async () => {
  const { c } = navigation()
  const lead = { id: 'lead', name: 'lead', address: 'repo:lead' }
  const child = { id: 'child', name: 'child', address: 'repo:child', parent: 'lead' }
  c.hierarchy = [lead, child]
  Object.defineProperty(c, 'feedViews', { get: () => feedModel(c.hierarchy) })
  c.view = 'channel:lead'
  c.feed = { room: { id: B }, posts: [] }
  c.openRun = ''
  c.reconcileRows = reconcileRows
  c.api.openRoom = async () => ({ ...opened(B), members: [lead, { ...child, parent: '' }] })
  for (const name of ['parentKey', 'noteParents', 'applyOpened', 'resync']) runDeclaration(c, name)
  await c.resync()
  assert.deepEqual(c.feedViews.leads, [])
  assert.equal(c.view, GENERAL)
})

test('a reconnect after a promote rereads the views of loaded completions in a history window', async () => {
  const { c } = navigation()
  const lead = { id: 'lead', name: 'lead', address: 'repo:lead' }
  const child = { id: 'child', name: 'child', address: 'repo:child', parent: 'lead' }
  const done = { id: 'done', from: child.address, completion: true, views: ['channel:lead'] }
  Object.defineProperty(c, 'feedViews', { get: () => feedModel(c.hierarchy) })
  Object.assign(c, { view: 'channel:lead', openRun: '', reconcileRows, rerouteAnchors, withViews, routedParents: '', feedAtBottom: false, workflowData: { loadJobs() {} }, ...appLoaderStubs(), roomSessionController: null, drill: null })
  for (const name of ['loadReviewer', 'loadChanges', 'loadGoals', 'loadRules', 'loadVisuals'] as const) c[name] = () => {}
  for (const name of ['parentKey', 'noteParents', 'rerouteLoaded', 'applyOpened', 'resync']) runDeclaration(c, name)
  c.roomId = B
  c.feed = { room: { id: B }, posts: [] }
  c.applyOpened({ ...opened(B), members: [lead, child] })
  // The owner reads history: newer posts are missing, and a completion is loaded.
  c.feed = { room: { id: B }, posts: [done], newer: 30 }
  // While the stream is down, the only child is promoted. The reconnect's tail
  // has neither the loaded post nor the old parent.
  c.api.openRoom = async () => ({ ...opened(B), members: [lead, { ...child, parent: '' }] })
  c.api.feedAround = async () => ({ room: { id: B }, posts: [{ ...done, views: ['general'] }] })
  await c.resync()
  await setImmediate()
  assert.equal(c.view, GENERAL)
  assert.ok(c.feed)
  assert.deepEqual(c.feed.posts.find((p) => p.id === 'done')?.views, ['general'])
})

test('changing rooms resets multi-selection and closes its previous consolidation screen', async () => {
  const { c } = navigation()
  c.selectingSessions = true; c.pickedSessions = ['one', 'two']
  c.consolidating = { roomId: B, sessions: ['one', 'two'] }
  assert.equal(await c.selectRoom(''), true)
  assert.equal(c.selectingSessions, false)
  assert.equal(c.pickedSessions.length, 0)
  assert.equal(c.consolidating, null)
})

test('opening a source post closes its transcript and overlays before showing the room post', async () => {
  const { c, calls } = navigation()
  c.drill = { id: 'worker' }
  c.card = { id: 'worker' }
  c.viewing = { id: 'attachment', postId: 'msg_stored' }
  c.feed = { room: { id: B }, posts: [{ id: 'msg_stored', from: 'repo:worker', to: ['repo'] }] }
  await c.jumpTo('msg_stored')
  assert.equal(c.drill, null)
  assert.equal(c.card, null)
  assert.equal(c.viewing, null)
  assert.deepEqual(calls, ['post'])
})

test('a link to a voice call line keeps the current view', async () => {
  const { c, calls } = navigation()
  c.view = 'channel:lead'
  c.feed = { room: { id: B }, posts: [{ id: 'msg_call', from: 'repo:voice-call', to: ['repo'], views: ['voice-call'] }] }
  await c.jumpTo('msg_call')
  assert.equal(c.view, 'channel:lead')
  assert.deepEqual(calls, [])
})

test('copied source-post links open the global room', async () => {
  const { c, calls } = navigation()
  c.rooms.push({ id: 'global' })
  c.location.hash = postHref('global', 'msg_stored')
  c.api.openRoom = (id) => Promise.resolve(opened(id))
  assert.equal(c.hashTarget(), true)
  await setImmediate()
  assert.equal(c.roomId, 'global')
  assert.deepEqual(calls, ['post'])
})

test('opening a view keeps only the newest posts of a long feed', () => {
  const { c } = navigation()
  c.feed = { room: { id: B }, earlier: 0, posts: Array.from({ length: 700 }, (_, i) => ({ id: `p${i}` })) }
  c.openView(GENERAL)
  assert.ok(c.feed)
  assert.equal(c.feed.posts.length, 300)
  assert.equal(c.feed.posts[0]?.id, 'p400')
  assert.equal(c.feed.earlier, 400)
})

test('selecting the open view again keeps older posts the reader is looking at', () => {
  const { c } = navigation()
  c.feed = { room: { id: B }, earlier: 0, posts: Array.from({ length: 700 }, (_, i) => ({ id: `p${i}` })) }
  c.feedAtBottom = false
  c.openView(GENERAL)
  assert.ok(c.feed)
  assert.equal(c.feed.posts.length, 700)
  // At the bottom, the same view trims as usual.
  c.feedAtBottom = true
  c.openView(GENERAL)
  assert.equal(c.feed.posts.length, 300)
})

test('a pin leaves the bottom before it loads older posts, so a live post cannot trim them', async () => {
  const { c } = navigation()
  let atBottom = true
  c.feed = { room: { id: B }, posts: [], earlier: 1, cursor: 'newer-result' }
  c.loadEarlier = async () => { atBottom = c.feedAtBottom; return false }
  await c.jumpTo('old-result')
  assert.equal(atBottom, false)
})

test('an older page cannot merge after a same-room refresh changes its boundary', async () => {
  const pending = deferred()
  const before = { room: { id: B }, posts: [{ id: 'p151' }, { id: 'p200' }], cursor: 'p151', earlier: 150 }
  const refreshed = { room: { id: B }, posts: [{ id: 'p181' }, { id: 'p250' }], cursor: 'p181', earlier: 180 }
  interface EarlierState {
    roomId: string
    roomRequest: number
    earlierLoad: unknown
    feed: NavFeed | null
    feedAtBottom: boolean
    STEP: number
    mergeFeedPage: typeof mergeFeedPage
    api: { feedTail: (id: string, cursor: string) => Promise<unknown> }
    loadEarlier: (...args: unknown[]) => unknown
  }
  const state: EarlierState = { roomId: B, roomRequest: 1, earlierLoad: null, feed: before, feedAtBottom: true, STEP: 50, mergeFeedPage, api: { feedTail: (id, cursor) => { assert.equal(cursor, 'p151'); return pending.promise } }, loadEarlier() {} }
  installRoomLoader(state, state.api)
  const c = state as EarlierState
  const loading = c.loadEarlier()
  c.feed = refreshed
  pending.resolve({ room: { id: B }, posts: [{ id: 'p51' }, { id: 'p150' }], cursor: 'p51', earlier: 50 })
  assert.equal(await loading, false)
  assert.equal(c.feed, refreshed)
  assert.equal(c.earlierLoad, null)
})


test('an issue link opens Issues and keeps its address through initial navigation', async () => {
  const { c, pending } = navigation()
  c.location.hash = issueHash(A, 839)
  assert.equal(c.hashTarget(), true)
  pending.resolve(opened(A))
  await setImmediate()
  assert.equal(c.roomId, A)
  assert.equal(c.view, 'issues')
  assert.deepEqual({ ...c.prLink }, { roomId: A, issue: 839 })
  assert.equal(c.location.hash, issueHash(A, 839))
})

test('Back and Forward between issues stay with the open Issues view', () => {
  const { c } = navigation()
  c.view = 'issues'
  c.openTarget = () => { throw new Error('Issues follows its own link') }
  c.location.hash = issueHash(B, 839)
  assert.equal(c.hashTarget(), true)
  assert.equal(c.location.hash, issueHash(B, 839))
})


test('an issue timeline PR link records the PR address for reload and Back', () => {
  const { c } = navigation()
  c.view = 'issues'
  c.location.hash = issueHash(B, 839)
  c.openPR(841)
  assert.equal(c.view, GENERAL)
  assert.equal(c.reviewTab, 'prs')
  assert.equal(c.location.hash, prHash(B, 841))
  assert.deepEqual({ ...c.prLink }, { roomId: B, pr: '841', tab: 'conversation' })
})

for (const itemHash of [prHash(B, 844), issueHash(B, 444)]) {
  test(`a pinned post or run gets a new entry after the item address ${itemHash}, so Back reopens the item`, async () => {
    const run = 'wf_20261017-120000-0000000b'
    for (const kind of ['post', 'shell', 'run']) {
      const { c } = navigation()
      c.location.hash = itemHash
      c.view = itemHash.includes('issue=') ? 'issues' : 'github'
      c.prLink = { roomId: B }
      c.feed = { room: { id: B }, posts: [{ id: 'pin', from: kind === 'shell' ? 'shell:sh_20261017-120000-0000000a' : 'owner', kind: 'failed', completion: true }] }
      if (kind === 'run') c.showRun({ id: run })
      else await c.jumpTo('pin')
      c.syncRoute()
      assert.equal(c.prLink, null)
      const view = kind === 'run' ? 'runs' : kind === 'shell' ? 'jobs' : GENERAL
      assert.equal(c.view, view)
      assert.deepEqual(c.entries, [['push', kind === 'run' ? `#room=${B}&run=${run}` : routeHash({ room: B, view, session: '', turn: 0, run: '' })]])
    }
  })
}

test('the address follows a session, and Back to the room address closes it without a room reload', async () => {
  const { c, calls, applied } = navigation()
  c.location.hash = `#room=${B}`
  const m = { id: 'ses_20261017-100000-cccccccc', name: 'lead' }
  c.drill = m
  c.syncRoute()
  assert.deepEqual(c.entries, [['push', `#room=${B}&session=${m.id}`]])
  // Back: the browser shows the room address again and fires hashchange.
  c.location.hash = `#room=${B}`
  assert.equal(c.hashTarget(), true)
  assert.equal(c.routing, 1, 'the address waits while the link is followed')
  await setImmediate()
  assert.equal(c.routing, 0)
  assert.equal(c.drill, null)
  assert.equal(c.view, GENERAL)
  assert.deepEqual(applied, [], 'the open room is not reloaded')
  assert.deepEqual(calls, [])
  c.syncRoute()
  assert.equal(c.entries.length, 1, 'the landed screen matches its address')
})

test('Back to the room while a session address still loads keeps the room the reader went back to', async () => {
  const { c, calls } = navigation()
  const id = 'ses_20261017-100000-cccccccc'
  const loading = deferred<{ member: NavMember }>()
  c.api.transcript = () => { calls.push('transcript'); return loading.promise }
  c.location.hash = `#room=${B}&session=${id}`
  assert.equal(c.hashTarget(), true)
  await setImmediate()
  assert.deepEqual(calls, ['transcript'], 'the session address asks for its transcript')
  // Back before the transcript lands: the room address matches the open screen.
  c.location.hash = `#room=${B}`
  assert.equal(c.hashTarget(), true)
  await setImmediate()
  loading.resolve({ member: { id, name: 'lead' } })
  await setImmediate()
  assert.ok(!calls.includes('session'), 'the late transcript does not open over the room')
  c.syncRoute()
  assert.equal(c.location.hash, `#room=${B}`, 'the room keeps the address Back restored')
  assert.ok(c.entries.every(([kind]) => kind === 'replace'), 'no session entry is pushed')
})

test('a reloaded session address reopens the session and keeps one history entry', async () => {
  const { c, pending, calls } = navigation()
  const id = 'ses_20261017-100000-cccccccc'
  c.location.hash = `#room=${A}&session=${id}&turn=2`
  assert.equal(c.hashTarget(), true)
  c.syncRoute()
  assert.deepEqual(c.entries, [], 'nothing is written while the room loads')
  pending.resolve(opened(A))
  await setImmediate()
  assert.equal(c.roomId, A)
  assert.deepEqual(calls, ['transcript', 'session'])
})

test('a followed address that cannot open is corrected in place', async () => {
  const { c } = navigation()
  c.location.hash = `#room=${B}`
  c.view = GENERAL
  c.reviewOpen = true
  c.reviewTab = 'files'
  c.routeFix = false
  // The run address names a run; the screen lands on the room instead.
  c.showRun = () => {}
  c.location.hash = `#room=${B}&run=wf_20261017-100000-dddddddd`
  c.hashTarget()
  await setImmediate()
  c.syncRoute()
  assert.deepEqual(c.entries.at(-1), ['replace', `#room=${B}`])
})

test('an open PR keeps its own address; the route waits until the view leaves it', () => {
  const { c } = navigation()
  c.view = GENERAL; c.reviewOpen = true; c.reviewTab = 'prs'
  c.location.hash = prHash(B, 9)
  c.syncRoute()
  assert.deepEqual(c.entries, [])
  c.openView('files')
  c.syncRoute()
  assert.deepEqual(c.entries, [['push', `#room=${B}&review=files`]])
})

test('successful read batches refresh view counts through the existing room debounce', () => {
  const marker = declarations.find((n) => n.type === 'VariableDeclaration' && n.declarations.some((d) => d.id.type === 'Identifier' && d.id.name === 'readMarker'))
  assert.ok(marker && marker.type === 'VariableDeclaration', 'App declares readMarker')
  const argumentsOf = marker.declarations[0]?.init
  assert.ok(argumentsOf && argumentsOf.type === 'CallExpression', 'readMarker is created')
  const options = argumentsOf.arguments[1]
  assert.ok(options && options.type === 'ObjectExpression', 'readMarker takes options')
  const read = options.properties.find((p) => p.type === 'Property' && p.key.type === 'Identifier' && p.key.name === 'read')
  assert.ok(read && read.type === 'Property' && read.value.type === 'ArrowFunctionExpression', 'read is a handler')
  const refresh = declarations.find((n) => n.type === 'FunctionDeclaration' && n.id?.name === 'soonRooms')
  assert.ok(refresh, 'soonRooms')
  let timer: () => void = () => { throw new Error('no timer') }
  let rooms = 0
  const tails: string[] = []
  interface ReadState {
    roomRefreshes: Set<unknown>
    roomTimer: (() => void) | null
    roomId: string
    feed: { posts: { id: string }[] }
    setTimeout(fn: () => void): number
    clearTimeout(): void
    loadRooms(): void
    roomData: { loadUnread(): void }
    afterRead: () => void
  }
  const state: ReadState = {
    roomRefreshes: new Set(), roomTimer: null, roomId: A, feed: { posts: [{ id: 'one' }] },
    setTimeout(fn: () => void) { timer = fn; return 1 }, clearTimeout() {},
    loadRooms() { rooms++ }, roomData: { loadUnread() { tails.push('unread') } },
    afterRead: () => {},
  }
  const c = state
  vm.createContext(c)
  const refreshRange = sourceRange(refresh)
  const callbackRange = sourceRange(read.value)
  vm.runInContext(stripTypes(source.slice(refreshRange.start, refreshRange.end) + '\nvar afterRead = ' + source.slice(callbackRange.start, callbackRange.end)), c)
  c.afterRead()
  c.afterRead()
  assert.equal(rooms, 0)
  assert.deepEqual(tails, [])
  timer()
  assert.equal(rooms, 1)
  assert.deepEqual(tails, ['unread'])
  c.roomId = ''
  c.afterRead()
  timer()
  assert.deepEqual(tails, ['unread'], 'leaving the room drops its pending feed refresh')
})


test('a view count refresh preserves over 200 displayed posts while scrolled up', async () => {
  const posts = Array.from({ length: 250 }, (_, i) => ({ id: `post-${i}` }))
  interface ViewState {
    roomId: string
    roomRequest: number
    feedAtBottom: boolean
    roomTimer: (() => void) | null
    roomRefreshes: Set<unknown>
    feed: NavFeed
    applyItemEvent: () => boolean
    reloadsUnread: typeof reloadsUnread
    home: null
    drill: null
    roomSessionController: null
    loadMembers(): void
    loadRooms(): void
    setTimeout(fn: () => void): void
    clearTimeout(): void
    timer?: () => void
    roomData?: ReturnType<typeof createRoomData>
    handle: (event: unknown) => void
    applyUnread: (value: unknown) => void
  }
  const c: ViewState = {
    roomId: A, roomRequest: 1, feedAtBottom: false, roomTimer: null, roomRefreshes: new Set(),
    feed: { room: { id: A }, posts, offset: 900, earlier: 20, cursor: 'post-0', viewUnread: {} },
    applyItemEvent: () => false, reloadsUnread, home: null, drill: null, roomSessionController: null, loadMembers() {}, loadRooms() {},
    setTimeout(fn: () => void) { c.timer = fn }, clearTimeout() {},
    handle: () => {}, applyUnread: () => {},
  }
  vm.createContext(c)
  runDeclaration(c, 'soonRooms')
  runDeclaration(c, 'handle')
  runDeclaration(c, 'applyUnread')
  const requests: unknown[][] = []
  const roomApi: object = {
    feedTail: async (...args: unknown[]) => { requests.push(args); return { room: { id: A }, posts: posts.slice(-1), viewUnread: { 'channel:lead': 3 } } },
    openRoom: async () => ({ feed: { room: { id: A }, posts: posts.slice(-200) } }),
  }
  const roomHooks: object = { current: () => ({ room: c.roomId, visit: c.roomRequest }), unread: c.applyUnread,
    opened(value: { feed: NavFeed }) { c.feed = mergeFeedPage(c.feed, value.feed) }, error(e: unknown): never { throw e },
  }
  c.roomData = createRoomData(roomApi as Parameters<typeof createRoomData>[0], roomHooks as Parameters<typeof createRoomData>[1])
  c.handle({ kind: 'members', roomId: A })
  c.timer?.()
  await setImmediate()
  assert.equal(c.feed.posts, posts)
  assert.equal(c.feed.posts.length, 250)
  assert.equal(c.feed.cursor, 'post-0')
  assert.equal(c.feed.earlier, 20)
  assert.equal(c.feed.offset, 900)
  assert.equal(c.feed.viewUnread?.['channel:lead'], 3)
  assert.deepEqual(requests, [[A, '', 1]])
  c.roomData.destroy()
})

test('unread metadata from an earlier room visit cannot update the current sidebar', async () => {
  const wait = deferred()
  let current = { room: A, visit: 1 }, updates = 0
  const roomHooks: object = { current: () => current, unread() { updates++ } }
  const roomApi: object = { feedTail: () => wait.promise }
  const data = createRoomData(roomApi as Parameters<typeof createRoomData>[0], roomHooks as Parameters<typeof createRoomData>[1])
  const loading = data.loadUnread()
  current = { room: A, visit: 2 }
  wait.resolve({ room: { id: A }, viewUnread: { 'channel:lead': 3 } })
  await loading
  assert.equal(updates, 0)
  data.destroy()
})

test('an older page cannot trim the post a reader moved to while it was loading', async () => {
 const {c}=navigation()
 const original={room:{id:B},posts:Array.from({length:600},(_,i)=>({id:'p'+i})),earlier:50,cursor:'p0'}
 const pending=deferred();let near=true
 Object.assign(c,{feed:original,earlierLoad:null,STEP:50})
 c.api.feedTail=()=>pending.promise
 runDeclaration(c,'loadEarlier')
 const loading=c.loadEarlier('older','',()=>near)
 near=false
 pending.resolve({room:{id:B},posts:[{id:'older'}],earlier:0})
 await loading
 assert.equal(c.feed,original)
})

test('opening a goal that no loaded page holds loads it, and a done goal opens under Done', async () => {
  const { c } = navigation()
  const { goalFilter } = await import('../goals/goals.ts')
  const old = { id: 'g9', state: 'done', text: 'Old goal' }
  const calls: unknown[][] = []
  c.goals = [{ id: 'g1', state: 'active' }]
  c.openView = () => {}
  c.api.goal = async (room, id) => { calls.push([room, id]); return old }
  c.fetchedGoal = null
  c.fetchedGoalVersion = 0
  for (const name of ['goalById', 'openGoal', 'trackFetchedGoal', 'refreshFetchedGoal']) runDeclaration(c, name)
  assert.equal(await c.openGoal('g9'), old)
  assert.equal(c.goalId, 'g9')
  assert.deepEqual(calls, [[B, 'g9']])
  // An unrelated goals refresh rebuilds the list without the old goal.
  c.goals = [{ id: 'g1', state: 'active' }, { id: 'g2', state: 'active' }]
  assert.equal(c.goalById('g9'), old, 'the fetched goal stays selectable')
  // An edit to the old goal arrives as a change event, or after a reload.
  c.trackFetchedGoal({ op: 'upsert', id: 'g9', item: { ...old, text: 'Edited' } })
  assert.equal(c.goalById('g9').text, 'Edited')
  c.api.goal = async (room, id) => { calls.push([room, id]); return { ...old, text: 'Reloaded' } }
  await c.refreshFetchedGoal()
  assert.equal(c.goalById('g9').text, 'Reloaded')
  // A change event that lands while a refresh is in flight wins over its
  // older answer.
  let answer: (value: NavGoal) => void = () => { throw new Error('no pending goal read') }
  c.api.goal = () => new Promise<NavGoal>((resolve) => (answer = resolve))
  const refreshing = c.refreshFetchedGoal()
  c.trackFetchedGoal({ op: 'upsert', id: 'g9', item: { ...old, text: 'From the event', holders: ['lead'] } })
  answer({ ...old, text: 'Older answer' })
  await refreshing
  assert.equal(c.goalById('g9').text, 'From the event')
  assert.deepEqual([...c.goalById('g9').holders ?? []], ['lead'])
  calls.length = 1
  c.trackFetchedGoal({ op: 'delete', id: 'g9' })
  assert.equal(c.goalById('g9'), undefined)
  assert.equal(goalFilter(old), 'done')
  assert.equal(goalFilter({ state: 'dropped' }), 'done')
  assert.equal(goalFilter({ state: 'active' }), 'active')
  assert.equal((await c.openGoal('g1')).id, 'g1')
  assert.equal(calls.length, 1, 'a loaded goal opens without a call')
})

test('Back reopens a PR that a link or reload opened: the next screen pushes its own entry', async () => {
  const { c } = navigation()
  c.routeFix = true
  c.location.hash = prHash(B, 844)
  assert.equal(c.hashTarget(), true)
  await setImmediate()
  assert.equal(c.view, GENERAL)
  assert.equal(c.reviewTab, 'prs')
  c.syncRoute()
  assert.deepEqual(c.entries, [], 'the PR address is already the entry')
  c.openView('files')
  c.syncRoute()
  assert.deepEqual(c.entries, [['push', `#room=${B}&review=files`]])
})

test('a shell link followed in the open room still opens its log', async () => {
  const { c } = navigation()
  const shell = 'sh_20261017-100000-aaaaaaaa'
  c.location.hash = detailHref({ kind: 'shell', id: shell }, B)
  assert.equal(c.hashTarget(), true)
  await setImmediate()
  assert.equal(c.view, 'jobs')
  assert.equal(c.openJob, shell)
})

test('a pairing link on a paired device keeps the open room; an empty address opens all rooms', async () => {
  const { c } = navigation()
  c.location.hash = '#pair=ABC'
  c.hashChanged()
  assert.equal(c.roomId, B)
  assert.equal(c.location.hash, '')
  c.hashChanged()
  await setImmediate()
  assert.equal(c.roomId, '')
})
