import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import { parse } from 'svelte/compiler'
import { hasItemChange, patchListRows } from './listChanges.js'
import { reconcileRows, createReloadGate } from './reloadGate.js'
import { createRoomData } from '../rooms/roomData.js'
import { createWorkflowData } from '../workflows/workflowData.ts'
import { mergeListPage } from './listPages.js'
import { reloadsUnread } from './unreadReload.ts'

const update = (id, text, inHead) => ({ op: 'upsert', id, item: { id, text }, ...(inHead === undefined ? {} : { inHead }) })

test('an off-head update patches loaded history and keeps its row object', () => {
  const row = { id: 'old', text: 'Before' }, rows = [row]
  const patched = patchListRows(rows, update('old', 'After', false))
  assert.equal(patched[0], row)
  assert.equal(row.text, 'After')
})

test('an unknown off-head item stays out of a fresh first page', () => {
  const rows = []
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

const source = readFileSync(new URL('../../App.svelte', import.meta.url), 'utf8')
const functions = parse(source).instance.content.body.filter((node) => node.type === 'FunctionDeclaration' && ['applyListItem', 'applyListPage', 'applyItemEvent', 'handle', 'loadOlderLists', 'trackFetchedGoal', 'refreshFetchedGoal'].includes(node.id.name))
function app(rows) {
  const c = { olderLoading: false, roomId: 'room', roomRequest: 1, home: null, fetchedGoal: null, fetchedGoalVersion: 0, goals: rows, listPages: { goals: { items: rows, loadedOlder: true, cursor: 'older', older: true } }, hasItemChange, reloadsUnread, patchListRows, reconcileRows, mergeListPage, goalFetches: 0, questions: [], questionFetches: 0, loadRules() {}, soonRooms() {} }
  c.loadGoals = () => c.goalFetches++
  c.loadQuestions = () => c.questionFetches++
  c.changes = []
  c.changeFetches = 0
  c.reviewerLoads = 0
  c.loadChanges = () => c.changeFetches++
  c.reviewerSoon = () => c.reviewerLoads++
  c.reload = createReloadGate(() => ({ room: c.roomId, visit: c.roomRequest }))
  c.refreshHeldComments ??= () => {}
  vm.createContext(c)
  for (const node of functions) vm.runInContext(source.slice(node.start, node.end), c)
  c.hierarchy = []
  c.memberFetches = 0
  c.loadMembers = () => c.memberFetches++
  c.roomData = createRoomData({ members: async () => { c.memberFetches++; return [] }, mentions: async () => [] }, {
    current: () => ({ room: c.roomId, visit: c.roomRequest }),
    patchMembers: (change) => { c.hierarchy = patchListRows(c.hierarchy, change, (member) => member.id || member.address); return c.hierarchy },
    choices() {},
  })
  c.loadJobs = () => c.jobFetches++
  c.jobs = []
  c.jobFetches = 0
  c.api = { shellJobs: () => { c.jobFetches++; return Promise.resolve({ items: [] }) } }
  c.workflowData = createWorkflowData(c.api, {
    current: () => ({ room: c.roomId, visit: c.roomRequest }),
    jobs: (page, append) => { c.jobs = c.applyListPage('jobs', page, append) },
    patchJobs: (change) => { c.jobs = c.applyListItem('jobs', change, c.jobs) },
  })
  return c
}

test('a server item event updates the actual App row without a list fetch', async () => {
  const row = { id: 'old', text: 'Before' }, c = app([row])
  const event = JSON.parse('{"kind":"goals","roomId":"room","change":{"op":"upsert","id":"old","inHead":false,"item":{"id":"old","text":"After","state":"done"}}}')
  c.handle(event)
  await new Promise(setImmediate)
  assert.equal(c.goalFetches, 0)
  assert.equal(c.goals[0], row)
  assert.equal(c.goals[0].text, 'After')
  assert.equal(c.listPages.goals.cursor, 'older')
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
  const row = { id: 'q1', state: 'open', text: 'Before' }
  c.questions = [row]
  c.handle(JSON.parse('{"kind":"questions","roomId":"room","change":{"op":"upsert","id":"q1","item":{"id":"q1","state":"answered","text":"After","answer":{"text":"Done"}}}}'))
  await new Promise(setImmediate)
  assert.equal(c.questionFetches, 0)
  assert.equal(c.questions[0], row)
  assert.equal(row.state, 'answered')
})


test('a shell job event patches loaded history through the workflow gate without a fetch', async () => {
  const c = app([])
  const row = { id: 'job', state: 'running', started: '1' }
  c.jobs = [row]
  c.listPages.jobs = { items: c.jobs, cursor: 'older', loadedOlder: true }
  c.handle(JSON.parse('{"kind":"jobs","roomId":"room","change":{"op":"upsert","id":"job","inHead":false,"item":{"id":"job","state":"exited","started":"1"}}}'))
  await new Promise(setImmediate)
  assert.equal(c.jobFetches, 0)
  assert.equal(c.jobs[0], row)
  assert.equal(row.state, 'exited')
  assert.equal(c.listPages.jobs.cursor, 'older')
  c.workflowData.destroy()
})


test('changes item events patch the list without fetching it', async () => {
  const c = app([])
  const row = { key: 'pr', title: 'Before' }
  c.changes = [row]
  c.listPages.changes = { items: c.changes, cursor: 'older', loadedOlder: true }
  c.handle(JSON.parse(JSON.stringify({ kind: 'changes', roomId: 'room', change: { op: 'upsert', id: 'pr', inHead: false, item: { key: 'pr', title: 'After' } } })))
  await new Promise(setImmediate)
  assert.equal(c.changeFetches, 0)
  assert.equal(c.changes[0], row)
  assert.equal(row.title, 'After')
  assert.equal(c.listPages.changes.cursor, 'older')
  assert.equal(c.reviewerLoads, 1)
  c.workflowData.destroy()
})

test('prs events fetch no list, because the PR rows come from Changes', async () => {
  const c = app([])
  const row = { key: 'pr', title: 'Before' }
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
  const row = { id: 'member', name: 'before' }
  c.hierarchy = [row]
  c.handle(JSON.parse('{"kind":"members","roomId":"room","change":{"op":"upsert","id":"member","item":{"id":"member","name":"after"}}}'))
  await new Promise(setImmediate)
  assert.equal(c.memberFetches, 0)
  assert.equal(c.hierarchy[0], row)
  assert.equal(row.name, 'after')
  c.roomData.destroy()
  c.workflowData.destroy()
})


for (const [kind, counter] of [['goals', 'goalFetches'], ['questions', 'questionFetches'], ['members', 'memberFetches'], ['jobs', 'jobFetches'], ['changes', 'changeFetches']]) {
  test(kind + ' upserts without an item keep the gated list fallback', async () => {
    const c = app([])
    c.handle({ kind, roomId: 'room', change: { op: 'upsert', id: 'missing-item' } })
    await new Promise(setImmediate)
    assert.equal(c[counter], 1)
    c.roomData.destroy()
    c.workflowData.destroy()
  })
}

for (const kind of ['goals', 'changes', 'jobs']) for (const operation of ['upsert', 'delete']) {
  test(kind + ' history response retains an item ' + operation + ' received while loading', async () => {
    const c = app([]), key = kind === 'changes' ? 'key' : 'id'
    const head = { [key]: 'head', title: 'Head', started: '2' }
    c[kind] = [head]
    c.listPages[kind] = { items: c[kind], older: true, cursor: 'older' }
    let finish, calls = 0
    const pending = new Promise((resolve) => { finish = resolve })
    const method = { goals: 'goals', changes: 'changes', jobs: 'shellJobs' }[kind]
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
