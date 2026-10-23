import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import { parse } from 'svelte/compiler'
import { need } from '../../../test/need.ts'
import { sourceRange, stripTypes } from '../../../test/svelte-source.ts'

// The machine list and page live in App.svelte. These run their real code with
// a fake api, so only the answer ordering is under test.
const source = readFileSync(new URL('../../App.svelte', import.meta.url), 'utf8')
const nodes = parse(source, { modern: true }).instance?.content.body ?? []

function functionCode(name: string): string {
  const node = nodes.find((n) => n.type === 'FunctionDeclaration' && n.id?.name === name)
  assert.ok(node, `App declares ${name}`)
  const { start, end } = sourceRange(node)
  return source.slice(start, end)
}

interface State {
  machines: unknown[] | null
  machinesRequest: number
  machineId: string
  machineDetail: unknown
  machineError: string
  machineRequest: number
}

function app(state: Partial<State>, api: object) {
  const c = { machines: null, machinesRequest: 0, machineId: '', machineDetail: null, machineError: '', machineRequest: 0, ...state, api, errorText: (e: unknown) => String(e) }
  vm.createContext(c)
  vm.runInContext(stripTypes(functionCode('loadMachines') + '\n' + functionCode('loadMachine')), c)
  return Object.assign(c, vm.runInContext('({ loadMachines, loadMachine })', c)) as State & {
    loadMachines: () => Promise<void>
    loadMachine: (id: string) => Promise<void>
  }
}

// settle lets the promise callbacks run without a timer.
const settle = () => new Promise((r) => setImmediate(r))

test('a slow machine list answer never replaces a newer one', async () => {
  const answers: ((list: unknown[]) => void)[] = []
  const c = app({}, { machines: () => new Promise((r) => answers.push(r)) })
  c.loadMachines()
  c.loadMachines()
  assert.equal(answers.length, 2, 'both reads are in flight')
  answers[1]?.([{ id: 'new' }])
  await settle()
  need(answers[0])([{ id: 'old' }])
  await settle()
  assert.deepEqual(c.machines, [{ id: 'new' }])
})

test('a page answer for a machine that closed is dropped', async () => {
  const answers: ((detail: unknown) => void)[] = []
  const c = app({ machineId: 'm_1' }, { machine: () => new Promise((r) => answers.push(r)) })
  c.loadMachine('m_1')
  c.machineId = 'm_2'
  need(answers[0])({ id: 'm_1', name: 'first' })
  await settle()
  assert.equal(c.machineDetail, null, 'the closed page does not come back')
})

// probeResultOf runs the page's own wording of one probe result.
function probeResultOf(detail: unknown): unknown {
  const c: Record<string, unknown> = {}
  vm.createContext(c)
  vm.runInContext(stripTypes(functionCode('probeResult')), c)
  const out = (vm.runInContext('probeResult', c) as (detail: unknown) => unknown)(detail)
  // The value is made in another VM context, so compare its data, not its prototype.
  return JSON.parse(JSON.stringify(out))
}

test('a degraded probe reports the disk hold, not an unreachable machine', () => {
  assert.deepEqual(probeResultOf({ name: 'hermeslab', status: 'degraded', statusReason: 'low-disk' }), {
    tone: 'error',
    subject: 'hermeslab is low on disk',
    text: 'Dyna starts no CI work here until space is freed.',
  })
  assert.deepEqual(probeResultOf({ name: 'hermeslab', status: 'degraded', statusReason: 'other' }), {
    tone: 'error',
    subject: 'hermeslab needs attention',
    text: 'Open the machine page to see why.',
  })
  assert.deepEqual(probeResultOf({ name: 'hermeslab', status: 'offline' }), {
    tone: 'error',
    subject: 'hermeslab did not answer the probe',
    text: 'Check the SSH route, then probe again.',
  })
  assert.deepEqual(probeResultOf({ name: 'hermeslab', status: 'online' }), { tone: 'success', subject: 'Probed hermeslab' })
})
