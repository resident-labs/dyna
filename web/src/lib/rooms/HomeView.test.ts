import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import { setImmediate } from 'node:timers/promises'
import { createReloadGate } from '../ui/reloadGate.ts'
import { hasItemChange, patchListRows } from '../ui/listChanges.ts'
import { parse } from 'svelte/compiler'
import { sourceRange, stripTypes } from '../../../test/svelte-source.ts'

// A home state gains the functions that the component source defines for it.
// vm runs untyped JavaScript, so each test names the functions it calls.
function homeFunctions<T>(code: string, state: object, names: readonly string[]): T {
  vm.createContext(state)
  vm.runInContext(stripTypes(code), state)
  return vm.runInContext(`({ ${names.join(', ')} })`, state) as T
}

// Run the real refresh code from Home.svelte and the real Settings onclose
// handler from App.svelte. The Svelte parser selects them without copying.
const homeSource = readFileSync(new URL('./Home.svelte', import.meta.url), 'utf8')
const homeBody = parse(homeSource, { modern: true }).instance?.content.body ?? []
const declarations: object[] = homeBody.flatMap((n) => {
  const declaration = n.type === 'ExportNamedDeclaration' ? n.declaration : n
  return declaration ? [declaration] : []
})
const sourceOf = (picked: readonly object[]): string => picked.map((n) => {
  const { start, end } = sourceRange(n)
  return homeSource.slice(start, end)
}).join('\n')
const isFunction = (entry: object, name: string): boolean => {
  const n = entry as Record<string, unknown>
  return n.type === 'FunctionDeclaration' && typeof n.id === 'object' && n.id !== null && Reflect.get(n.id, 'name') === name
}
const declares = (entry: object, name: string): boolean => {
  const n = entry as Record<string, unknown>
  return n.type === 'VariableDeclaration' && Array.isArray(n.declarations) && n.declarations.some((d: unknown) => typeof d === 'object' && d !== null && typeof Reflect.get(d, 'id') === 'object' && Reflect.get(d, 'id') !== null && Reflect.get(Reflect.get(d, 'id') as object, 'name') === name)
}
const refreshCode = sourceOf(declarations.filter((n) => isFunction(n, 'refresh') || declares(n, 'accountsRequest')))

const appSource = readFileSync(new URL('../../App.svelte', import.meta.url), 'utf8')
// The template walk reads only node names, attributes and source ranges.
type Node = Record<string, unknown>
const asNode = (value: unknown): Node | undefined => (value !== null && typeof value === 'object' ? value as Node : undefined)
function range(node: unknown): { start: number; end: number } {
  if (typeof node !== 'object' || node === null || typeof Reflect.get(node, 'start') !== 'number' || typeof Reflect.get(node, 'end') !== 'number') throw new Error('the node has no source range')
  return { start: Reflect.get(node, 'start') as number, end: Reflect.get(node, 'end') as number }
}
function findSettingsClose(node: unknown): string | null {
  if (!node || typeof node !== 'object') return null
  const item = node as Node
  if (item.type === 'InlineComponent' && item.name === 'Settings') {
    const attributes = Array.isArray(item.attributes) ? item.attributes.map(asNode).filter((a) => a !== undefined) : []
    const attr = attributes.find((a) => a.name === 'onclose')
    const value = attr?.value
    const expr = asNode(Array.isArray(value) ? value[0] : value)?.expression
    const { start, end } = range(expr)
    return appSource.slice(start, end)
  }
  for (const v of Object.values(item)) {
    const found = Array.isArray(v) ? v.map(findSettingsClose).find(Boolean) : findSettingsClose(v)
    if (found) return found
  }
  return null
}
const onclose = findSettingsClose(parse(appSource).html)
assert.ok(onclose, 'App wires the Settings onclose handler')

interface Usage {
  id?: string
  n?: number
  accounts?: { id: string; needsLogin: boolean }[]
}

interface CloseState {
  usage: Usage | null
  runtimes: { v: string } | null
  api: { accountUsageCached: () => Promise<Usage>; privateRuntimes: () => Promise<{ v: string }> }
  refresh: () => Promise<void>
}

test('closing Settings reloads the home screen accounts and runtimes', async () => {
  let n = 0
  const state = { usage: { accounts: [{ id: 'g', needsLogin: true }] } as Usage, runtimes: { v: 'old' }, api: {
    accountUsageCached: () => Promise.resolve({ accounts: [{ id: 'g', needsLogin: false }], n: ++n }),
    privateRuntimes: () => Promise.resolve({ v: 'new' }),
  } }
  const c: CloseState = Object.assign(state, homeFunctions<Pick<CloseState, 'refresh'>>(refreshCode, state, ['refresh']))

  // Settings is where the owner signs in to Codex, so the
  // header's Call Voice rereads whether voice is set up.
  let voiceRefreshed = 0
  const app = { showSettings: true, loadProfiles: () => {}, home: { refresh: () => c.refresh() }, voiceCall: { refresh: () => voiceRefreshed++ } }
  vm.createContext(app)
  vm.runInContext(stripTypes(`(${onclose})()`), app)
  await setImmediate()
  assert.equal(app.showSettings, false)
  assert.equal(voiceRefreshed, 1)
  assert.equal(c.usage?.accounts?.[0]?.needsLogin, false)
  assert.equal(c.runtimes?.v, 'new')
})

test('a late account answer cannot replace a newer one', async () => {
  let resolveOld: (value: Usage) => void = () => { throw new Error('no pending account read') }
  const old = new Promise<Usage>((r) => { resolveOld = r })
  let calls = 0
  const state = { usage: null as Usage | null, runtimes: null as { v: string } | null, api: {
    accountUsageCached: () => (++calls === 1 ? old : Promise.resolve({ id: 'new' })),
    privateRuntimes: () => new Promise<{ v: string }>(() => {}),
  } }
  const c: CloseState = Object.assign(state, homeFunctions<Pick<CloseState, 'refresh'>>(refreshCode, state, ['refresh']))
  c.refresh()
  c.refresh()
  await setImmediate()
  resolveOld({ id: 'old' })
  await setImmediate()
  assert.equal(c.usage?.id, 'new')
})

const questionCode = sourceOf(declarations.filter((n) => isFunction(n, 'loadQuestions') || isFunction(n, 'refreshQuestionRooms') || isFunction(n, 'applyQuestionEvent') || isFunction(n, 'event')))

interface Question {
  id: string
  state: string
  text?: string
}

interface HomeRoom {
  id: string
  archived?: boolean
  lastActivity?: number
}

interface QuestionsState {
  questions: Record<string, Question[]>
  questionsLoaded?: boolean
  questionRooms?: string | null
  rooms?: HomeRoom[]
  refresh?(): void
  loadNotices?(): void
  loadPRs?(): void
  hasItemChange?: typeof hasItemChange
  patchListRows?: typeof patchListRows
  questionReload?: ReturnType<typeof createReloadGate>
  api: { questions: (ref: string) => Promise<Record<string, Question[]>> }
}

interface QuestionsApp extends QuestionsState {
  loadQuestions: () => Promise<void>
  refreshQuestionRooms: () => Promise<void>
  event: (change: { kind: string; roomId?: string; change?: { op: string; id?: string; item?: Question } }) => void
}

function questionsApp<S extends QuestionsState>(state: S): S & QuestionsApp {
  const fns = homeFunctions<Pick<QuestionsApp, 'loadQuestions' | 'refreshQuestionRooms' | 'event'>>(questionCode, state, ['loadQuestions', 'refreshQuestionRooms', 'applyQuestionEvent', 'event'])
  return Object.assign(state, fns)
}

test('home uses one grouped question request and ignores post events', async () => {
  const calls: string[] = [], grouped = { a: [{ id: 'q1', state: 'open' }], b: [{ id: 'q2', state: 'open' }] }
  const state: QuestionsState = { questions: { stale: [] }, refresh() {}, loadNotices() {}, api: {
    questions: async (ref: string) => { calls.push(ref); return grouped },
  } }
  state.hasItemChange = hasItemChange
  state.patchListRows = patchListRows
  state.questionReload ??= createReloadGate(() => ({ room: 'home', visit: state.questionRooms }))
  const c = questionsApp(state)
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
  const calls: string[] = []
  const state: QuestionsState = { rooms: [{ id: 'room', archived: true }], questionRooms: null, questions: {}, api: {
    questions: async (ref: string): Promise<Record<string, Question[]>> => {
      calls.push(ref)
      return state.rooms?.[0]?.archived ? {} : { room: [{ id: 'q1', state: 'open' }] }
    },
  } }
  state.hasItemChange = hasItemChange
  state.patchListRows = patchListRows
  state.questionReload ??= createReloadGate(() => ({ room: 'home', visit: state.questionRooms }))
  const c = questionsApp(state)
  await c.refreshQuestionRooms()
  assert.deepEqual(Object.keys(c.questions), [])
  c.rooms = [{ id: 'room', archived: false, lastActivity: 1 }]
  await c.refreshQuestionRooms()
  assert.equal(c.questions.room?.[0]?.id, 'q1')
  c.rooms = [{ id: 'room', archived: false, lastActivity: 2 }]
  await c.refreshQuestionRooms()
  assert.deepEqual(calls, ['', ''])
})


test('home applies question items after an in-flight grouped snapshot without refetching', async () => {
  let finish: (value: Record<string, Question[]>) => void = () => { throw new Error('no pending grouped question read') }
  let calls = 0
  const state: QuestionsState = { rooms: [{ id: 'a' }], questionRooms: 'a', questions: {}, patchListRows, refresh() {}, loadNotices() {},
    api: { questions: () => { calls++; return new Promise<Record<string, Question[]>>((resolve) => { finish = resolve }) } } }
  state.questionReload = createReloadGate(() => ({ room: 'home', visit: state.questionRooms }))
  state.hasItemChange = hasItemChange
  state.patchListRows = patchListRows
  state.questionReload ??= createReloadGate(() => ({ room: 'home', visit: state.questionRooms }))
  const c = questionsApp(state)
  const loading = c.loadQuestions()
  const finishInitial = finish
  c.event(JSON.parse('{"kind":"questions","roomId":"a","change":{"op":"upsert","id":"q1","item":{"id":"q1","state":"answered","text":"Done"}}}'))
  finishInitial({ a: [{ id: 'q1', state: 'open', text: 'Old' }] })
  await loading
  assert.equal(calls, 1)
  assert.equal(c.questions.a?.length, 0)
  c.event(JSON.parse('{"kind":"questions","roomId":"a","change":{"op":"upsert","id":"q2","item":{"id":"q2","state":"open","text":"New"}}}'))
  await setImmediate()
  assert.equal(c.questions.a?.[0]?.id, 'q2')
  assert.equal(calls, 1)
})

test('home counts questions as loaded only after a successful grouped request', async () => {
  for (const [name, questions, loaded] of [['rejected', async () => { throw new Error('offline') }, false], ['answered', async () => ({}), true]] as [string, () => Promise<Record<string, Question[]>>, boolean][]) {
    const state: QuestionsState = { questions: {}, questionsLoaded: false, refresh() {}, loadNotices() {}, loadPRs() {}, api: { questions } }
    state.hasItemChange = hasItemChange
    state.patchListRows = patchListRows
    state.questionReload ??= createReloadGate(() => ({ room: 'home', visit: state.questionRooms }))
    const c = questionsApp(state)
    const pending = c.loadQuestions()
    assert.equal(c.questionsLoaded, false, `${name}: not loaded while the request runs`)
    await pending
    assert.equal(c.questionsLoaded, loaded, name)
  }
})

const noticeCode = sourceOf(declarations.filter((n) => isFunction(n, 'loadNotices') || declares(n, 'noticesRequest')))

interface Notice {
  id: string
  text: string
}

interface NoticesState {
  notices: Notice[]
  noticesLoaded: boolean
  api: { notices: () => Promise<Notice[]> }
  loadNotices: () => Promise<void>
}

test('a failed notice refresh keeps the last notices and is not counted as loaded', async () => {
  const alert = [{ id: 'n1', text: 'Disk almost full' }]
  let answer = async () => alert
  const state = { notices: [], noticesLoaded: false, api: { notices: () => answer() } }
  const c: NoticesState = Object.assign(state, homeFunctions<Pick<NoticesState, 'loadNotices'>>(noticeCode, state, ['loadNotices']))
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
  const state: QuestionsState = { questions: {}, questionsLoaded: false, questionRooms: null, rooms: [{ id: 'a' }], refresh() {}, loadNotices() {}, api: { questions: async () => ({}) } }
  state.hasItemChange = hasItemChange
  state.patchListRows = patchListRows
  state.questionReload ??= createReloadGate(() => ({ room: 'home', visit: state.questionRooms }))
  const c = questionsApp(state)
  await c.refreshQuestionRooms()
  assert.equal(c.questionsLoaded, true)
  c.api.questions = async () => { throw new Error('offline') }
  c.rooms = [{ id: 'a' }, { id: 'b' }]
  await c.refreshQuestionRooms()
  assert.equal(c.questionsLoaded, false)
})
