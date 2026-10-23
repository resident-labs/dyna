import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import { parse } from 'svelte/compiler'
import { createReloadGate, reconcileRows } from '../ui/reloadGate.ts'
import { sourceRange, stripTypes } from '../../../test/svelte-source.ts'

const source = readFileSync(new URL('../../App.svelte', import.meta.url), 'utf8')
const nodes = parse(source, { modern: true }).instance?.content.body ?? []
const functionCode = (name: string) => {
  const node = nodes.find((n) => n.type === 'FunctionDeclaration' && n.id?.name === name)
  if (!node) return ''
  const { start, end } = sourceRange(node)
  return source.slice(start, end)
}

interface AllRoom {
  id: string
  members: number
}

interface AgentsState {
  pageVisible: boolean
  agentsNav: boolean
  narrow: boolean
  showSide: boolean
  navOpen: boolean
  agentsRequest: number
  agentMembers: Record<string, unknown[]>
  agentGoals: Record<string, unknown>
}

interface AgentsApp extends AgentsState {
  api: { members: (room: string) => Promise<unknown[]>; goals: () => Promise<{ items: unknown[] }> }
  loadAgents: (rooms: AllRoom[]) => Promise<void>
}

function app(state: Partial<AgentsState>, members: (room: string) => Promise<unknown[]>): AgentsApp {
  const c = {
    pageVisible: true, agentsNav: true, narrow: false, showSide: true, navOpen: false,
    agentsRequest: 0, agentMembers: {} as Record<string, unknown[]>, agentGoals: {} as Record<string, unknown>, createReloadGate, reconcileRows,
    ...state, api: { members, goals: async () => ({ items: [] }) },
  }
  vm.createContext(c)
  vm.runInContext(stripTypes(functionCode('agentsVisible')), c)
  const binding = nodes.find((n) => n.type === 'VariableDeclaration' && n.declarations.some((d) => d.id.type === 'Identifier' && d.id.name === 'reloadAgents'))
  assert.ok(binding, 'App declares reloadAgents')
  const { start, end } = sourceRange(binding)
  vm.runInContext(stripTypes(source.slice(start, end) + '\n' + functionCode('loadAgents')), c)
  const fns = vm.runInContext('({ loadAgents })', c) as Pick<AgentsApp, 'loadAgents'>
  return Object.assign(c, fns)
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
  let finish: (rooms: unknown[]) => void = () => { throw new Error('no pending Members request') }
  let calls = 0
  const pending = new Promise<unknown[]>((resolve) => { finish = resolve })
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
