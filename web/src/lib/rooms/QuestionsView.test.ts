import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import { createReloadGate, reconcileRows } from '../ui/reloadGate.ts'
import { parse } from 'svelte/compiler'
import { sourceRange, stripTypes } from '../../../test/svelte-source.ts'

const source = readFileSync(new URL('./Questions.svelte', import.meta.url), 'utf8')
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
  focusId: string
  focusRequest: number
  questions: QuestionRow[]
  filter: string
  focus: (id: string) => number
  applyFocusRequest: () => void
}

test('live question refreshes preserve the chosen filter and Reply focus', () => {
  const calls: string[] = []
  const c: QuestionsState = { focusId: 'q1', focusRequest: 1, questions: [{ id: 'q1', state: 'open' }], filter: 'open', focus: (id: string) => calls.push(id), applyFocusRequest() {} }
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

const appSource = readFileSync(new URL('../../App.svelte', import.meta.url), 'utf8')
const load = (parse(appSource, { modern: true }).instance?.content.body ?? []).find((n) => n.type === 'FunctionDeclaration' && n.id?.name === 'loadQuestions')
assert.ok(load, 'App has loadQuestions')
const loadRange = sourceRange(load)

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
  vm.createContext(c)
  vm.runInContext(stripTypes(appSource.slice(loadRange.start, loadRange.end)), c)
  const { loadQuestions } = vm.runInContext('({ loadQuestions })', c) as { loadQuestions: () => Promise<unknown> }
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
const renderedCode = await bundle({ component: '../rooms/Questions' })

test('free-text Send requires content, submits once, and keeps a failed draft for retry', async () => {
  const dom = installDom()
  globalThis.HTMLMediaElement = dom.window.HTMLMediaElement
  const ui = await importBundle(renderedCode, '../rooms/Questions')
  let finish: () => void = () => { throw new Error('no pending answer') }
  let fail: (reason: unknown) => void = () => { throw new Error('no pending answer') }
  const sent: unknown[][] = []
  const component = ui.mount(ui.default, { target: document.getElementById('app'), props: {
    roomId: 'r', questions: [{ id: 'q1', to: 'owner', state: 'open', askerName: 'agent', askerHue: 1, text: 'What should we do?', meanwhile: 'Reviewing', askedAt: '2026-10-22T00:00:00Z', options: [] }],
    onanswer: (...args: unknown[]) => { sent.push(args); return new Promise<void>((resolve, reject) => { finish = resolve; fail = reject }) },
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
