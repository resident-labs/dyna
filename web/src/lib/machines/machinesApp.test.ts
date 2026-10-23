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
  machineBusy: boolean
  machineProbing: boolean
}

// app runs the page's real machine code over a fake api. extra holds the
// context the code reads besides state, such as the toast.
function app(state: Partial<State>, api: object, extra: Record<string, unknown> = {}) {
  const c = {
    machines: null, machinesRequest: 0, machineId: '', machineDetail: null, machineError: '', machineRequest: 0,
    machineBusy: false, machineProbing: false, ...state, ...extra, api, errorText: (e: unknown) => String(e),
  }
  vm.createContext(c)
  const code = [functionCode('loadMachines'), functionCode('loadMachine'), functionCode('probeResult'), functionCode('probeMachine')].join('\n')
  vm.runInContext(stripTypes(code), c)
  return Object.assign(c, vm.runInContext('({ loadMachines, loadMachine, probeMachine })', c)) as State & {
    loadMachines: () => Promise<void>
    loadMachine: (id: string) => Promise<void>
    probeMachine: () => Promise<void>
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

// probeToasts runs the probe action over one API answer and returns what the
// page said. A value made in the VM context is compared as data, not prototype.
async function probeToasts(detail: unknown): Promise<unknown> {
  const said: unknown[] = []
  const c = app(
    { machineId: 'm_1', machineDetail: { name: 'hermeslab' } },
    { probeMachine: () => Promise.resolve(detail), machines: () => Promise.resolve([]) },
    { say: (toast: unknown) => said.push(JSON.parse(JSON.stringify(toast))), loadMachines: () => {} },
  )
  await c.probeMachine()
  return said
}

test('a degraded probe reports the disk hold, not an unreachable machine', async () => {
  assert.deepEqual(await probeToasts({ name: 'hermeslab', status: 'degraded', statusReason: 'low-disk' }), [
    { tone: 'error', subject: 'hermeslab is low on disk', text: 'Dyna starts no CI work here until space is freed.' },
  ])
  assert.deepEqual(await probeToasts({ name: 'hermeslab', status: 'degraded', statusReason: 'other' }), [
    { tone: 'error', subject: 'hermeslab needs attention', text: 'Open the machine page to see why.' },
  ])
  assert.deepEqual(await probeToasts({ name: 'hermeslab', status: 'offline' }), [
    { tone: 'error', subject: 'hermeslab did not answer the probe', text: 'Check the SSH route, then probe again.' },
  ])
  assert.deepEqual(await probeToasts({ name: 'hermeslab', status: 'online' }), [{ tone: 'success', subject: 'Probed hermeslab' }])
})
