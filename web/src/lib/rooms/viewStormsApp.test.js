import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import { parse } from 'svelte/compiler'
import { createReloadGate, reconcileRows } from '../ui/reloadGate.ts'

const source = readFileSync(new URL('../../App.svelte', import.meta.url), 'utf8')
const nodes = parse(source).instance.content.body
const functionCode = (name) => {
  const node = nodes.find((n) => n.type === 'FunctionDeclaration' && n.id.name === name)
  return node ? source.slice(node.start, node.end) : ''
}

function app(state, members) {
  const c = {
    pageVisible: true, agentsNav: true, narrow: false, showSide: true, navOpen: false,
    agentsRequest: 0, agentMembers: {}, agentGoals: {}, createReloadGate, reconcileRows,
    ...state, api: { members, goals: async () => ({ items: [] }) },
  }
  vm.createContext(c)
  vm.runInContext(functionCode('agentsVisible'), c)
  const binding = nodes.find((n) => n.type === 'VariableDeclaration' && n.declarations.some((d) => d.id.name === 'reloadAgents'))
  vm.runInContext(source.slice(binding.start, binding.end) + '\n' + functionCode('loadAgents'), c)
  return c
}

for (const layout of [
  { name: 'closed wide sidebar', narrow: false, showSide: false, navOpen: false },
  { name: 'closed phone drawer', narrow: true, showSide: true, navOpen: false },
  { name: 'hidden browser tab', narrow: false, showSide: true, navOpen: false, pageVisible: false },
]) test(`Home skips all-room Members for a ${layout.name}`, async () => {
  let calls = 0
  const c = app(layout, async () => { calls++; return [] })
  const rooms = Array.from({ length: 36 }, (_, i) => ({ id: String(i), members: 1 }))
  await c.loadAgents(rooms)
  assert.equal(calls, 0)
  c.pageVisible = true
  c.showSide = true
  c.navOpen = true
  await c.loadAgents(rooms)
  assert.equal(calls, 36, 'the visible agents sidebar still needs every room')
})

test('closing the agents sidebar discards an old all-room Members response and its trailing refresh', async () => {
  let finish, calls = 0
  const pending = new Promise((resolve) => { finish = resolve })
  const c = app({}, () => { calls++; return pending })
  const rooms = [{ id: 'a', members: 1 }]
  const first = c.loadAgents(rooms)
  c.loadAgents(rooms)
  c.showSide = false
  finish([{ id: 'old' }])
  await first
  assert.equal(calls, 1)
  assert.deepEqual(Object.keys(c.agentMembers), [])
})
