import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import { createReloadGate, reconcileRows } from '../ui/reloadGate.ts'
import { parse } from 'svelte/compiler'

const source = readFileSync(new URL('./Questions.svelte', import.meta.url), 'utf8')
const code = parse(source).instance.content.body.filter((n) =>
  n.type === 'FunctionDeclaration' && n.id.name === 'applyFocusRequest' ||
  n.type === 'VariableDeclaration' && n.declarations.some((d) => d.id.name === 'appliedFocus'),
).map((n) => source.slice(n.start, n.end)).join('\n')

test('live question refreshes preserve the chosen filter and Reply focus', () => {
  const calls = []
  const c = { focusId: 'q1', focusRequest: 1, questions: [{ id: 'q1', state: 'open' }], filter: 'open', focus: (id) => calls.push(id) }
  vm.createContext(c)
  vm.runInContext(code, c)
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
const load = parse(appSource).instance.content.body.find((n) => n.type === 'FunctionDeclaration' && n.id.name === 'loadQuestions')

test('a delayed question snapshot cannot replace a newer answer', async () => {
  let resolveOld
  const old = new Promise((resolve) => { resolveOld = resolve })
  let calls = 0
  const c = {
    reconcileRows, roomId: 'room', questionsRequest: 0, questions: [], visuals: [], loadError: '',
    api: { questions: () => ++calls === 1 ? old : Promise.resolve({ room: [{ id: 'q1', state: 'answered' }] }), visuals: () => Promise.resolve([]) },
  }
  c.reload = createReloadGate(() => ({ room: c.roomId, visit: 1 }))
  vm.createContext(c)
  vm.runInContext(appSource.slice(load.start, load.end), c)
  const pending = c.loadQuestions()
  const answered = c.loadQuestions()
  assert.equal(calls, 1)
  resolveOld({ room: [{ id: 'q1', state: 'open' }] })
  await Promise.all([pending, answered])
  assert.equal(calls, 2)
  assert.equal(c.questions[0].state, 'answered')
})

const { bundle, importBundle, installDom, domNames } = await import('../prs/prDetailHarness.ts')
const { rm } = await import('node:fs/promises')
const renderedCode = await bundle({ component: '../rooms/Questions' })

test('free-text Send requires content, submits once, and keeps a failed draft for retry', async () => {
  const dom = installDom()
  globalThis.HTMLMediaElement = dom.window.HTMLMediaElement
  const ui = await importBundle(renderedCode, '../rooms/Questions')
  let finish, fail
  const sent = []
  const component = ui.mount(ui.default, { target: document.getElementById('app'), props: {
    roomId: 'r', questions: [{ id: 'q1', to: 'owner', state: 'open', askerName: 'agent', askerHue: 1, text: 'What should we do?', meanwhile: 'Reviewing', askedAt: '2026-10-22T00:00:00Z', options: [] }],
    onanswer: (...args) => { sent.push(args); return new Promise((resolve, reject) => { finish = resolve; fail = reject }) },
  } })
  const settle = async () => { for (let i = 0; i < 4; i++) { await Promise.resolve(); ui.flushSync() } }
  try {
    ui.flushSync()
    const input = document.querySelector('input')
    const button = document.querySelector('button[type=submit]')
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
    document.querySelector('form').requestSubmit(); ui.flushSync()
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
