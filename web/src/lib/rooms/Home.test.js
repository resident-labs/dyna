import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import { setImmediate } from 'node:timers/promises'
import { createReloadGate } from '../ui/reloadGate.ts'
import { hasItemChange, patchListRows } from '../ui/listChanges.ts'
import { parse } from 'svelte/compiler'

// Run the real refresh code from Home.svelte and the real Settings onclose
// handler from App.svelte. The Svelte parser selects them without copying.
const homeSource = readFileSync(new URL('./Home.svelte', import.meta.url), 'utf8')
const refreshCode = parse(homeSource).instance.content.body
  .map((n) => (n.type === 'ExportNamedDeclaration' ? n.declaration : n))
  .filter((n) => n?.type === 'FunctionDeclaration' && n.id.name === 'refresh' || n?.type === 'VariableDeclaration' && n.declarations.some((d) => d.id.name === 'accountsRequest'))
  .map((n) => homeSource.slice(n.start, n.end)).join('\n')

const appSource = readFileSync(new URL('../../App.svelte', import.meta.url), 'utf8')
function findSettingsClose(node) {
  if (!node || typeof node !== 'object') return null
  if (node.type === 'InlineComponent' && node.name === 'Settings') {
    const attr = node.attributes.find((a) => a.name === 'onclose')
    const expr = attr.value[0].expression
    return appSource.slice(expr.start, expr.end)
  }
  for (const v of Object.values(node)) {
    const found = Array.isArray(v) ? v.map(findSettingsClose).find(Boolean) : findSettingsClose(v)
    if (found) return found
  }
  return null
}
const onclose = findSettingsClose(parse(appSource).html)

test('closing Settings reloads the home screen accounts and runtimes', async () => {
  let n = 0
  const c = { usage: { accounts: [{ id: 'g', needsLogin: true }] }, runtimes: { v: 'old' } }
  c.api = {
    accountUsageCached: () => Promise.resolve({ accounts: [{ id: 'g', needsLogin: false }], n: ++n }),
    privateRuntimes: () => Promise.resolve({ v: 'new' }),
  }
  vm.createContext(c)
  vm.runInContext(refreshCode, c)

  // Settings is where the owner signs in to Codex, so the
  // header's Call Voice rereads whether voice is set up.
  let voiceRefreshed = 0
  const app = { showSettings: true, loadProfiles: () => {}, home: { refresh: () => c.refresh() }, voiceCall: { refresh: () => voiceRefreshed++ } }
  vm.createContext(app)
  vm.runInContext(`(${onclose})()`, app)
  await setImmediate()
  assert.equal(app.showSettings, false)
  assert.equal(voiceRefreshed, 1)
  assert.equal(c.usage.accounts[0].needsLogin, false)
  assert.equal(c.runtimes.v, 'new')
})

test('a late account answer cannot replace a newer one', async () => {
  let resolveOld
  const old = new Promise((r) => { resolveOld = r })
  let calls = 0
  const c = { usage: null, runtimes: null, api: {
    accountUsageCached: () => (++calls === 1 ? old : Promise.resolve({ id: 'new' })),
    privateRuntimes: () => new Promise(() => {}),
  } }
  vm.createContext(c)
  vm.runInContext(refreshCode, c)
  c.refresh()
  c.refresh()
  await setImmediate()
  resolveOld({ id: 'old' })
  await setImmediate()
  assert.equal(c.usage.id, 'new')
})

const questionCode = parse(homeSource).instance.content.body
  .map((n) => n.type === 'ExportNamedDeclaration' ? n.declaration : n)
  .filter((n) => n?.type === 'FunctionDeclaration' && ['loadQuestions', 'refreshQuestionRooms', 'applyQuestionEvent', 'event'].includes(n.id.name))
  .map((n) => homeSource.slice(n.start, n.end)).join('\n')

test('home uses one grouped question request and ignores post events', async () => {
  const calls = [], grouped = { a: [{ id: 'q1', state: 'open' }], b: [{ id: 'q2', state: 'open' }] }
  const c = { questions: { stale: [] }, refresh() {}, loadNotices() {}, api: {
    questions: async (ref) => { calls.push(ref); return grouped },
  } }
  c.hasItemChange = hasItemChange
  c.patchListRows = patchListRows
  c.questionReload ??= createReloadGate(() => ({ room: 'home', visit: c.questionRooms }))
  vm.createContext(c)
  vm.runInContext(questionCode, c)
  await c.loadQuestions()
  assert.deepEqual(calls, [''])
  assert.equal(c.questions, grouped)
  c.event({ kind: 'posts', roomId: 'a' })
  await setImmediate()
  assert.equal(calls.length, 1)
  c.event({ kind: 'questions', roomId: 'a' })
  await setImmediate()
  assert.deepEqual(calls, ['', ''])
})

test('unarchiving a room reloads its existing question without refetching for activity changes', async () => {
  const calls = []
  const c = { rooms: [{ id: 'room', archived: true }], questionRooms: null, questions: {}, api: {
    questions: async (ref) => {
      calls.push(ref)
      return c.rooms[0].archived ? {} : { room: [{ id: 'q1', state: 'open' }] }
    },
  } }
  c.hasItemChange = hasItemChange
  c.patchListRows = patchListRows
  c.questionReload ??= createReloadGate(() => ({ room: 'home', visit: c.questionRooms }))
  vm.createContext(c)
  vm.runInContext(questionCode, c)
  await c.refreshQuestionRooms()
  assert.deepEqual(Object.keys(c.questions), [])
  c.rooms = [{ id: 'room', archived: false, lastActivity: 1 }]
  await c.refreshQuestionRooms()
  assert.equal(c.questions.room[0].id, 'q1')
  c.rooms = [{ id: 'room', archived: false, lastActivity: 2 }]
  await c.refreshQuestionRooms()
  assert.deepEqual(calls, ['', ''])
})


test('home applies question items after an in-flight grouped snapshot without refetching', async () => {
  let finish
  let calls = 0
  const c = { rooms: [{ id: 'a' }], questionRooms: 'a', questions: {}, patchListRows, refresh() {}, loadNotices() {},
    api: { questions: () => { calls++; return new Promise((resolve) => { finish = resolve }) } } }
  c.questionReload = createReloadGate(() => ({ room: 'home', visit: c.questionRooms }))
  c.hasItemChange = hasItemChange
  c.patchListRows = patchListRows
  c.questionReload ??= createReloadGate(() => ({ room: 'home', visit: c.questionRooms }))
  vm.createContext(c)
  vm.runInContext(questionCode, c)
  const loading = c.loadQuestions()
  const finishInitial = finish
  c.event(JSON.parse('{"kind":"questions","roomId":"a","change":{"op":"upsert","id":"q1","item":{"id":"q1","state":"answered","text":"Done"}}}'))
  finishInitial({ a: [{ id: 'q1', state: 'open', text: 'Old' }] })
  await loading
  assert.equal(calls, 1)
  assert.equal(c.questions.a.length, 0)
  c.event(JSON.parse('{"kind":"questions","roomId":"a","change":{"op":"upsert","id":"q2","item":{"id":"q2","state":"open","text":"New"}}}'))
  await setImmediate()
  assert.equal(c.questions.a[0].id, 'q2')
  assert.equal(calls, 1)
})

test('home counts questions as loaded only after a successful grouped request', async () => {
  for (const [name, questions, loaded] of [['rejected', async () => { throw new Error('offline') }, false], ['answered', async () => ({}), true]]) {
    const c = { questions: {}, questionsLoaded: false, refresh() {}, loadNotices() {}, loadPRs() {}, api: { questions } }
    c.hasItemChange = hasItemChange
    c.patchListRows = patchListRows
    c.questionReload ??= createReloadGate(() => ({ room: 'home', visit: c.questionRooms }))
    vm.createContext(c)
    vm.runInContext(questionCode, c)
    const pending = c.loadQuestions()
    assert.equal(c.questionsLoaded, false, `${name}: not loaded while the request runs`)
    await pending
    assert.equal(c.questionsLoaded, loaded, name)
  }
})

const noticeCode = parse(homeSource).instance.content.body
  .filter((n) => n.type === 'FunctionDeclaration' && n.id.name === 'loadNotices' || n.type === 'VariableDeclaration' && n.declarations.some((d) => d.id.name === 'noticesRequest'))
  .map((n) => homeSource.slice(n.start, n.end)).join('\n')

test('a failed notice refresh keeps the last notices and is not counted as loaded', async () => {
  const alert = [{ id: 'n1', text: 'Disk almost full' }]
  let answer = async () => alert
  const c = { notices: [], noticesLoaded: false, api: { notices: () => answer() } }
  vm.createContext(c)
  vm.runInContext(noticeCode, c)
  await c.loadNotices()
  assert.equal(c.noticesLoaded, true)
  answer = async () => { throw new Error('offline') }
  const failing = c.loadNotices()
  assert.equal(c.noticesLoaded, false, 'not loaded while the refresh runs')
  await failing
  assert.equal(c.noticesLoaded, false)
  assert.deepEqual(c.notices, alert, 'the last known alert stays visible')
})

test('a room membership change invalidates questions until the new load succeeds', async () => {
  const c = { questions: {}, questionsLoaded: false, questionRooms: null, rooms: [{ id: 'a' }], refresh() {}, loadNotices() {}, api: { questions: async () => ({}) } }
  c.hasItemChange = hasItemChange
  c.patchListRows = patchListRows
  c.questionReload ??= createReloadGate(() => ({ room: 'home', visit: c.questionRooms }))
  vm.createContext(c)
  vm.runInContext(questionCode, c)
  await c.refreshQuestionRooms()
  assert.equal(c.questionsLoaded, true)
  c.api.questions = async () => { throw new Error('offline') }
  c.rooms = [{ id: 'a' }, { id: 'b' }]
  await c.refreshQuestionRooms()
  assert.equal(c.questionsLoaded, false)
})
