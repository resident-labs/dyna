import { test } from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { parse } from 'svelte/compiler'
import vm from 'node:vm'
import { createReloadGate, reconcileRows } from '../ui/reloadGate.ts'
import { stripTypes } from '../../../test/svelte-source.ts'

const source = readFileSync(new URL('../../App.svelte', import.meta.url), 'utf8')
const body = parse(source).instance.content.body
const fn = (name) => body.find((n) => n.type === 'FunctionDeclaration' && n.id.name === name)
function app(names, c) {
  vm.createContext(c)
  for (const n of names) vm.runInContext(stripTypes(source.slice(fn(n).start, fn(n).end)), c)
  return c
}

test('opening a room loads its issues, so a first open and a reconnect both show them', () => {
  assert.match(source.slice(fn('applyOpened').start, fn('applyOpened').end), /loadIssues\(\)/)
})

test('the History list drops open issues that the all=true call returns', async () => {
  const c = {
    roomId: 'a', issues: [], issueHistory: [], showIssues: true, loadError: '', reconcileRows,
    api: { issues: async () => [{ id: 'o', state: 'open', changedAt: 0 }, { id: 'c', state: 'closed', changedAt: 5 }, { id: 'g', state: 'goal', changedAt: 6 }] },
  }
  c.reload = createReloadGate(() => ({ room: 'a', visit: 1 }), () => true)
  app(['loadIssues'], c)
  await c.loadIssues(true)
  assert.deepEqual(c.issueHistory.map((i) => i.id), ['c', 'g'])
})

test('Close asks for a reason and sends it', async () => {
  const calls = []
  const c = {
    roomId: 'a', ask: null, loadError: '', showIssues: false, say() {},
    api: { closeIssue: async (...a) => { calls.push(a) } },
    loadIssues() {},
  }
  app(['triageIssue', 'askCloseIssue'], c)
  c.askCloseIssue({ id: 'i1', title: 'Pin menu loses focus' })
  assert.equal(c.ask.action, 'Close')
  assert.equal(c.ask.label, 'Reason')
  await c.ask.submit('duplicate of i0')
  assert.deepEqual(calls, [['a', 'i1', 'duplicate of i0']])
  assert.equal(c.ask, null)
})
