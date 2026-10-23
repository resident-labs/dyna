import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import { hasItemChange, patchListRows } from './listChanges.ts'
import type { RowChange } from './listChanges.ts'
import type { ListChange } from '../api/event.ts'
import { createReloadGate, reconcileRows } from './reloadGate.ts'
import { createRoomData } from '../rooms/roomData.ts'
import { createWorkflowData } from '../workflows/workflowData.ts'
import type { WorkflowApi, WorkflowHooks } from '../workflows/workflowData.ts'
import { mergeListPage } from './listPages.ts'
import { reloadsUnread } from './unreadReload.ts'
import { topLevelFunctions } from '../../../test/svelte-source.ts'
import { member } from '../../../test/session-fixtures.ts'
import type { Member } from '../api/room.ts'

// One row shape for every list these tests patch.
interface AppRow {
  id?: string
  key?: string
  text?: string
  state?: string
  title?: string
  started?: string
  name?: string
  address?: string
}

interface AppListPage {
  items: AppRow[]
  cursor: string
  older?: boolean
  loadedOlder?: boolean
}

type ListKind = 'goals' | 'questions' | 'members' | 'jobs' | 'changes'

const update = (id: string, text: string, inHead?: boolean) => ({ op: 'upsert', id, item: { id, text }, ...(inHead === undefined ? {} : { inHead }) })

test('an off-head update patches loaded history and keeps its row object', () => {
  const row: AppRow = { id: 'old', text: 'Before' }, rows = [row]
  const patched = patchListRows(rows, update('old', 'After', false))
  assert.equal(patched[0], row)
  assert.equal(row.text, 'After')
})

test('an unknown off-head item stays out of a fresh first page', () => {
  const rows: AppRow[] = []
  assert.equal(patchListRows(rows, update('old', 'History', false)), rows)
  assert.equal(rows.length, 0)
})

test('a new head item and an unpaged item enter the list', () => {
  for (const inHead of [true, undefined]) {
    assert.equal(patchListRows([], update('new', 'New item', inHead))[0]?.id, 'new')
  }
})

test('a delete removes the item from any loaded page', () => {
  assert.equal(patchListRows([{ id: 'old' }], { op: 'delete', id: 'old' }).length, 0)
})

// The App functions these tests drive, as the vm installs them on the sandbox.
interface AppFunctions {
  handle: (event: unknown) => void
  loadOlderLists: (kinds: ListKind[]) => Promise<void>
  applyListPage: (kind: string, page: AppListPage, append?: boolean) => AppRow[]
  applyListItem: (kind: string, change: unknown, rows: AppRow[]) => AppRow[]
}

// The sandbox is the App state these functions read, plus the stubs that count
// the loads the App would make.
interface AppState {
  mergeVersion: number
  olderLoading: boolean
  roomId: string
  roomRequest: number
  home: null
  fetchedGoal: null
  fetchedGoalVersion: number
  goals: AppRow[]
  listPages: Partial<Record<ListKind, AppListPage>>
  hasItemChange: typeof hasItemChange
  reloadsUnread: typeof reloadsUnread
  patchListRows: typeof patchListRows
  reconcileRows: typeof reconcileRows
  mergeListPage: typeof mergeListPage
  goalFetches: number
  questions: AppRow[]
  questionFetches: number
  loadRules: () => void
  soonRooms: () => void
  loadGoals: () => void
  loadQuestions: () => void
  changes: AppRow[]
  changeFetches: number
  reviewerLoads: number
  loadChanges: () => void
  reviewerSoon: () => void
  reload: ReturnType<typeof createReloadGate>
  refreshHeldComments: () => void
  hierarchy: Member[]
  memberFetches: number
  loadMembers: () => void
  jobs: AppRow[]
  jobFetches: number
  [name: string]: unknown
}

// The sandbox fills only the API calls these tests drive; the controller reads
// its full API and hook types.
interface FakeHooks {
  current: () => { room: string; visit: number }
  jobs?: (page: never, append: boolean) => void
  patchJobs?: (change: never) => void
}
const workflowData = (api: SandboxApi, hooks: FakeHooks) =>
  createWorkflowData(api as WorkflowApi, hooks as WorkflowHooks)

interface SandboxApi {
  goals?: (room: string, cursor: string) => Promise<AppListPage>
  changes?: (room: string, cursor: string) => Promise<AppListPage>
  shellJobs?: (room: string, before?: string) => Promise<AppListPage>
}

// bind calls an App function with the sandbox as its receiver, as App does.
function bind<A extends unknown[], R>(sandbox: AppState, name: string): (...args: A) => R {
  const fn = sandbox[name]
  if (typeof fn !== 'function') throw new Error(`App.svelte defines no ${name}`)
  return (...args: A): R => fn.apply(sandbox, args) as R
}

function appFunctions(c: AppState): AppFunctions {
  return {
    handle: bind<[unknown], void>(c, 'handle'),
    loadOlderLists: bind<[ListKind[]], Promise<void>>(c, 'loadOlderLists'),
    applyListPage: bind<[string, AppListPage, boolean?], AppRow[]>(c, 'applyListPage'),
    applyListItem: bind<[string, unknown, AppRow[]], AppRow[]>(c, 'applyListItem'),
  }
}

type AppSandbox = AppState & AppFunctions & {
  api: SandboxApi
  roomData: ReturnType<typeof createRoomData>
  workflowData: ReturnType<typeof createWorkflowData>
}

const source = readFileSync(new URL('../../App.svelte', import.meta.url), 'utf8')
const functions = topLevelFunctions(source, ['applyListItem', 'applyListPage', 'applyItemEvent', 'handle', 'loadOlderLists', 'trackFetchedGoal', 'refreshFetchedGoal'])
function app(rows: AppRow[]): AppSandbox {
  const c: AppState = {
    mergeVersion: 0, olderLoading: false, roomId: 'room', roomRequest: 1, home: null, fetchedGoal: null, fetchedGoalVersion: 0,
    goals: rows, listPages: { goals: { items: rows, loadedOlder: true, cursor: 'older', older: true } },
    hasItemChange, reloadsUnread, patchListRows, reconcileRows, mergeListPage,
    goalFetches: 0, questions: [], questionFetches: 0, loadRules() {}, soonRooms() {},
    loadGoals: () => { c.goalFetches++ },
    loadQuestions: () => { c.questionFetches++ },
    changes: [], changeFetches: 0, reviewerLoads: 0,
    loadChanges: () => { c.changeFetches++ },
    reviewerSoon: () => { c.reviewerLoads++ },
    reload: createReloadGate(() => ({ room: c.roomId, visit: c.roomRequest })),
    refreshHeldComments: () => {},
    hierarchy: [], memberFetches: 0,
    loadMembers: () => { c.memberFetches++ },
    jobs: [], jobFetches: 0,
    loadJobs: () => { c.jobFetches++ },
  }
  vm.createContext(c)
  for (const code of functions.values()) vm.runInContext(code, c)
  const installed = appFunctions(c)
  const roomData = createRoomData({ members: async () => { c.memberFetches++; return [] }, mentions: async () => [] }, {
    current: () => ({ room: c.roomId, visit: c.roomRequest }),
    members() {},
    patchMembers: (change: ListChange) => { c.hierarchy = patchListRows(c.hierarchy, change as RowChange<Member>, (row) => row.id || row.address); return c.hierarchy },
    choices() {},
  })
  const api: SandboxApi = { shellJobs: () => { c.jobFetches++; return Promise.resolve({ items: [], older: false, cursor: '' }) } }
  const data = workflowData(api, {
    current: () => ({ room: c.roomId, visit: c.roomRequest }),
    jobs: (page, append) => { c.jobs = installed.applyListPage('jobs', page, append) },
    patchJobs: (change) => { c.jobs = installed.applyListItem('jobs', change, c.jobs) },
  })
  return Object.assign(c, installed, { api, roomData, workflowData: data })
}

test('a server item event updates the actual App row without a list fetch', async () => {
  const row: AppRow = { id: 'old', text: 'Before' }, c = app([row])
  const event = JSON.parse('{"kind":"goals","roomId":"room","change":{"op":"upsert","id":"old","inHead":false,"item":{"id":"old","text":"After","state":"done"}}}')
  c.handle(event)
  await new Promise(setImmediate)
  assert.equal(c.goalFetches, 0)
  assert.equal(c.goals[0], row)
  assert.equal(c.goals[0]?.text, 'After')
  assert.equal(c.listPages.goals?.cursor, 'older')
  const fresh = app([])
  fresh.handle(event)
  await new Promise(setImmediate)
  assert.equal(fresh.goals.length, 0)
  assert.equal(fresh.goalFetches, 0)
})

test('an event without an item keeps the existing list reload', async () => {
  const c = app([])
  c.handle({ kind: 'goals', roomId: 'room' })
  await new Promise(setImmediate)
  assert.equal(c.goalFetches, 1)
})


test('question item events patch the actual App without a scoped request', async () => {
  const c = app([])
  const row: AppRow = { id: 'q1', state: 'open', text: 'Before' }
  c.questions = [row]
  c.handle(JSON.parse('{"kind":"questions","roomId":"room","change":{"op":"upsert","id":"q1","item":{"id":"q1","state":"answered","text":"After","answer":{"text":"Done"}}}}'))
  await new Promise(setImmediate)
  assert.equal(c.questionFetches, 0)
  assert.equal(c.questions[0], row)
  assert.equal(row.state, 'answered')
})


test('a shell job event patches loaded history through the workflow gate without a fetch', async () => {
  const c = app([])
  const row: AppRow = { id: 'job', state: 'running', started: '1' }
  c.jobs = [row]
  c.listPages.jobs = { items: c.jobs, cursor: 'older', loadedOlder: true }
  c.handle(JSON.parse('{"kind":"jobs","roomId":"room","change":{"op":"upsert","id":"job","inHead":false,"item":{"id":"job","state":"exited","started":"1"}}}'))
  await new Promise(setImmediate)
  assert.equal(c.jobFetches, 0)
  assert.equal(c.jobs[0], row)
  assert.equal(row.state, 'exited')
  assert.equal(c.listPages.jobs?.cursor, 'older')
  c.workflowData.destroy()
})


test('changes item events patch the list without fetching it', async () => {
  const c = app([])
  const row: AppRow = { key: 'pr', title: 'Before' }
  c.changes = [row]
  c.listPages.changes = { items: c.changes, cursor: 'older', loadedOlder: true }
  c.handle(JSON.parse(JSON.stringify({ kind: 'changes', roomId: 'room', change: { op: 'upsert', id: 'pr', inHead: false, item: { key: 'pr', title: 'After' } } })))
  await new Promise(setImmediate)
  assert.equal(c.changeFetches, 0)
  assert.equal(c.changes[0], row)
  assert.equal(row.title, 'After')
  assert.equal(c.listPages.changes?.cursor, 'older')
  assert.equal(c.reviewerLoads, 1)
  c.workflowData.destroy()
})

test('prs events fetch no list, because the PR rows come from Changes', async () => {
  const c = app([])
  const row: AppRow = { key: 'pr', title: 'Before' }
  c.changes = [row]
  c.handle({ kind: 'prs', roomId: 'room', change: { op: 'upsert', id: 'pr', item: { key: 'pr', title: 'After' } } })
  c.handle({ kind: 'prs', roomId: 'room' })
  await new Promise(setImmediate)
  assert.equal(c.changeFetches, 0)
  assert.equal(row.title, 'Before')
  assert.equal(c.reviewerLoads, 2)
  c.workflowData.destroy()
})


test('member items patch the actual App without fetching its roster', async () => {
  const c = app([])
  const row = member('member', { name: 'before' })
  c.hierarchy = [row]
  c.handle(JSON.parse('{"kind":"members","roomId":"room","change":{"op":"upsert","id":"member","item":{"id":"member","name":"after"}}}'))
  await new Promise(setImmediate)
  assert.equal(c.memberFetches, 0)
  assert.equal(c.hierarchy[0], row)
  assert.equal(row.name, 'after')
  c.roomData.destroy()
  c.workflowData.destroy()
})


for (const [kind, counter] of [['goals', 'goalFetches'], ['questions', 'questionFetches'], ['members', 'memberFetches'], ['jobs', 'jobFetches'], ['changes', 'changeFetches']] as const) {
  test(kind + ' upserts without an item keep the gated list fallback', async () => {
    const c = app([])
    c.handle({ kind, roomId: 'room', change: { op: 'upsert', id: 'missing-item' } })
    await new Promise(setImmediate)
    assert.equal(c[counter], 1)
    c.roomData.destroy()
    c.workflowData.destroy()
  })
}

const apiMethod = { goals: 'goals', changes: 'changes', jobs: 'shellJobs' } as const
for (const kind of ['goals', 'changes', 'jobs'] as const) for (const operation of ['upsert', 'delete']) {
  test(kind + ' history response retains an item ' + operation + ' received while loading', async () => {
    const c = app([]), key = kind === 'changes' ? 'key' : 'id'
    const head: AppRow = { [key]: 'head', title: 'Head', started: '2' }
    c[kind] = [head]
    c.listPages[kind] = { items: c[kind], older: true, cursor: 'older' }
    let finish: (page: AppListPage) => void = () => {}, calls = 0
    const pending = new Promise<AppListPage>((resolve) => { finish = resolve })
    const method = apiMethod[kind]
    c.api[method] = (room, cursor) => {
      calls++
      assert.equal(room, 'room')
      assert.equal(cursor, 'older')
      return pending
    }
    const loading = c.loadOlderLists([kind])
    await new Promise(setImmediate)
    assert.equal(calls, 1)
    c.handle({ kind, roomId: 'room', change: { op: operation, id: 'old', inHead: false,
      ...(operation === 'upsert' ? { item: { [key]: 'old', title: 'Updated', started: '1' } } : {}) } })
    finish({ items: [{ [key]: 'old', title: 'Stale', started: '1' }], older: false, cursor: '' })
    await loading
    await new Promise(setImmediate)
    const row = c[kind].find((item) => item[key] === 'old')
    if (operation === 'delete') assert.equal(row, undefined)
    else assert.equal(row?.title, 'Updated')
    assert.equal(c[kind].find((item) => item[key] === 'head'), head)
    assert.equal(calls, 1)
    c.roomData.destroy()
    c.workflowData.destroy()
  })
}
