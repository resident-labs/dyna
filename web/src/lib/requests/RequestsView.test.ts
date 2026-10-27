import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import { createReloadGate, reconcileRows } from '../ui/reloadGate.ts'
import { parse } from 'svelte/compiler'
import { sourceRange, stripTypes } from '../../../test/svelte-source.ts'
import { installQuestions } from '../../../test/appModules.ts'
import { requestFilter } from './requests.ts'

const source = readFileSync(new URL('./Requests.svelte', import.meta.url), 'utf8')
const code = (parse(source, { modern: true }).instance?.content.body ?? []).filter((n) =>
  n.type === 'FunctionDeclaration' && n.id?.name === 'applyFocusRequest' ||
  n.type === 'VariableDeclaration' && n.declarations.some((d) => d.id.type === 'Identifier' && d.id.name === 'appliedFocus'),
).map((n) => {
  const { start, end } = sourceRange(n)
  return source.slice(start, end)
}).join('\n')

interface QuestionRow {
  id: string
  state: string
}

interface QuestionsState {
  requestFilter: typeof requestFilter
  focusId: string
  focusRequest: number
  questions: QuestionRow[]
  filter: string
  focus: (id: string) => number
  applyFocusRequest: () => void
}

test('live question refreshes preserve the chosen filter and Reply focus', () => {
  const calls: string[] = []
  const c: QuestionsState = { requestFilter, focusId: 'q1', focusRequest: 1, questions: [{ id: 'q1', state: 'open' }], filter: 'open', focus: (id: string) => calls.push(id), applyFocusRequest() {} }
  vm.createContext(c)
  vm.runInContext(stripTypes(code), c)
  const fns = vm.runInContext('({ applyFocusRequest })', c) as Pick<QuestionsState, 'applyFocusRequest'>
  Object.assign(c, fns)
  c.applyFocusRequest()
  assert.deepEqual(calls, ['q1'])
  c.filter = 'answered'
  c.questions = [{ id: 'q1', state: 'answered' }]
  c.applyFocusRequest()
  assert.deepEqual(calls, ['q1'])
  assert.equal(c.filter, 'answered')
  c.focusRequest++
  c.applyFocusRequest()
  assert.deepEqual(calls, ['q1', 'q1'])
})

// The questions list lives in lib/app/questions.svelte.ts now, so the test
// installs that module over its own state.

interface QuestionsLoadState {
  roomId: string
  questionsRequest: number
  questions: QuestionRow[]
  visuals: unknown[]
  loadError: string
  reconcileRows: typeof reconcileRows
  reload?: ReturnType<typeof createReloadGate>
  api: { questions: () => Promise<{ room: QuestionRow[] }>; visuals: () => Promise<unknown[]> }
}

test('a delayed question snapshot cannot replace a newer answer', async () => {
  let resolveOld: (value: { room: QuestionRow[] }) => void = () => { throw new Error('no pending question read') }
  const old = new Promise<{ room: QuestionRow[] }>((resolve) => { resolveOld = resolve })
  let calls = 0
  const c: QuestionsLoadState = {
    reconcileRows, roomId: 'room', questionsRequest: 0, questions: [], visuals: [], loadError: '',
    api: { questions: () => ++calls === 1 ? old : Promise.resolve({ room: [{ id: 'q1', state: 'answered' }] }), visuals: () => Promise.resolve([]) },
  }
  c.reload = createReloadGate(() => ({ room: c.roomId, visit: 1 }))
  const loadQuestions = installQuestions(c).loadQuestions
  const pending = loadQuestions()
  const answered = loadQuestions()
  assert.equal(calls, 1)
  resolveOld({ room: [{ id: 'q1', state: 'open' }] })
  await Promise.all([pending, answered])
  assert.equal(calls, 2)
  assert.equal(c.questions[0]?.state, 'answered')
})

const { bundle, importBundle, installDom, domNames } = await import('../prs/prDetailHarness.ts')
const { rm } = await import('node:fs/promises')
const renderedCode = await bundle({ component: '../requests/Requests' })

test('a waiting-owner terminal row runs its command and declines with an optional reason', async () => {
  const dom = installDom()
  globalThis.HTMLMediaElement = dom.window.HTMLMediaElement
  const ui = await importBundle(renderedCode, '../requests/Requests')
  const ran: string[] = []
  const declined: string[][] = []
  const terminal = { argv: ['gh', 'auth', 'login', '--scopes', 'workflow'], cwd: '/tmp/work', reason: 'Sign in', stage: 'waiting-owner' }
  const component = ui.mount(ui.default, { target: document.getElementById('app'), props: {
    roomId: 'r', questions: [
      { id: 'q1', to: 'owner', state: 'open', askerName: 'agent', askerHue: 1, text: 'What should we do?', meanwhile: 'Reviewing', askedAt: '2026-10-22T00:00:00Z', options: [] },
      { id: 'q2', to: 'owner', state: 'open', kind: 'terminal', terminal, askerName: 'builder', askerHue: 2, text: 'Sign in', askedAt: '2026-10-22T00:01:00Z', options: [] },
      { id: 'q3', to: 'owner', state: 'open', kind: 'terminal', terminal: { ...terminal, stage: 'waiting-session', reason: 'Held request' }, askerName: 'builder', askerHue: 2, text: 'Held request', askedAt: '2026-10-22T00:02:00Z', options: [] },
    ],
    onanswer: () => {}, onrun: (id: string) => { ran.push(id) }, ondecline: (id: string, reason: string) => { declined.push([id, reason]) },
  } })
  const settle = async () => { for (let i = 0; i < 4; i++) { await Promise.resolve(); ui.flushSync() } }
  const button = (text: string) => [...document.querySelectorAll<HTMLButtonElement>('button')].find((b) => b.textContent === text)
  try {
    ui.flushSync()
    assert.equal(document.querySelectorAll('article').length, 2)
    assert.equal(document.body.textContent?.includes('Held request'), false)
    assert.ok(document.querySelector('.command')?.textContent?.includes('gh auth login --scopes workflow'))
    button('Review')?.click(); await settle()
    assert.deepEqual(ran, ['q2'])
    button('Decline…')?.click(); ui.flushSync()
    const reason = document.querySelector<HTMLInputElement>('input[aria-label="Reason for declining q2"]')
    assert.ok(reason)
    reason.value = 'Not needed'; reason.dispatchEvent(new Event('input', { bubbles: true })); ui.flushSync()
    document.querySelector<HTMLFormElement>('.decline-form')?.requestSubmit(); await settle()
    assert.deepEqual(declined, [['q2', 'Not needed']])
  } finally {
    await ui.unmount(component)
    dom.window.close()
    for (const name of [...domNames, 'HTMLMediaElement']) Reflect.deleteProperty(globalThis, name)
    await rm(ui.dir, { recursive: true, force: true })
  }
})

test('free-text Send requires content, submits once, and keeps a failed draft for retry', async () => {
  const dom = installDom()
  globalThis.HTMLMediaElement = dom.window.HTMLMediaElement
  const ui = await importBundle(renderedCode, '../requests/Requests')
  let finish: () => void = () => { throw new Error('no pending answer') }
  let fail: (reason: unknown) => void = () => { throw new Error('no pending answer') }
  const sent: unknown[][] = []
  const component = ui.mount(ui.default, { target: document.getElementById('app'), props: {
    roomId: 'r', questions: [{ id: 'q1', to: 'owner', state: 'open', askerName: 'agent', askerHue: 1, text: 'What should we do?', meanwhile: 'Reviewing', askedAt: '2026-10-22T00:00:00Z', options: [] }],
    onanswer: (...args: unknown[]) => { sent.push(args); return new Promise<void>((resolve, reject) => { finish = resolve; fail = reject }) },
    onrun: () => {}, ondecline: () => {},
  } })
  const settle = async () => { for (let i = 0; i < 4; i++) { await Promise.resolve(); ui.flushSync() } }
  try {
    ui.flushSync()
    const input = document.querySelector<HTMLInputElement>('input')
    const button = document.querySelector<HTMLButtonElement>('button[type=submit]')
    assert.ok(input && button)
    assert.equal(button.disabled, true)
    input.value = '  '; input.dispatchEvent(new Event('input', { bubbles: true })); ui.flushSync()
    assert.equal(button.disabled, true)
    input.value = 'Keep the draft'; input.dispatchEvent(new Event('input', { bubbles: true })); ui.flushSync()
    assert.equal(button.disabled, false)
    button.click(); button.click(); ui.flushSync()
    assert.deepEqual(sent, [['q1', '', 'Keep the draft']])
    assert.equal(button.disabled, true)
    fail(new Error('Try again')); await settle()
    assert.equal(input.value, 'Keep the draft')
    assert.equal(button.disabled, false)
    document.querySelector<HTMLFormElement>('form')?.requestSubmit(); ui.flushSync()
    assert.equal(sent.length, 2)
    finish(); await settle()
    assert.equal(input.value, '')
    assert.equal(button.disabled, true)
  } finally {
    await ui.unmount(component)
    dom.window.close()
    for (const name of [...domNames, 'HTMLMediaElement']) Reflect.deleteProperty(globalThis, name)
    await rm(ui.dir, { recursive: true, force: true })
  }
})
