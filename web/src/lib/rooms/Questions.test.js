import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import { createReloadGate, reconcileRows } from '../ui/reloadGate.js'
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
