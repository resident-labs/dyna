import test from 'node:test'
import assert from 'node:assert/strict'
import { FORMAT, bundle, importBundle, installDom } from '../prs/prDetailHarness.ts'
import type { MachineWork, MachineWorkItem } from '../api/machine.ts'

const code = await bundle({ component: '../machines/MachineWorkCard', format: `${FORMAT} export const duration = () => '2m05s';` })

async function mountCard(work: MachineWork | null) {
  installDom()
  const ui = await importBundle(code, '../machines/MachineWorkCard')
  const target = document.getElementById('app')
  assert.ok(target)
  const component = ui.mount(ui.default, { target, props: { work } })
  ui.flushSync()
  return { ui, component }
}

const unit = (over: Partial<MachineWorkItem>): MachineWorkItem => ({ kind: 'job', id: 'job_1', label: 'make test', cpus: 2, memoryMiB: 2048, heavy: false, since: new Date(Date.now() - 125_000).toISOString(), ...over })

const capacity = { cpus: 8, memoryMiB: 16384, cpusUsed: 3, memoryMiBUsed: 3072, diskFree: 60 * 2 ** 30, diskTotal: 100 * 2 ** 30, diskKeeps: 'a tenth of the disk', heavyRunning: 1, heavyLimit: 1 }

const rows = () => [...document.querySelectorAll('.text')].map((n) => n.textContent)

test('an idle node says nothing runs and no work waits', async () => {
  const { ui, component } = await mountCard({ capacity: { ...capacity, cpusUsed: 0, memoryMiBUsed: 0, heavyRunning: 0 }, running: [], waiting: [] })
  const text = document.body.textContent ?? ''
  assert.match(text, /Nothing runs on this node\./)
  assert.match(text, /No work waits\./)
  assert.match(text, /0 of 8 CPUs/)
  ui.unmount(component)
})

test('a check row names its check and PR without raw identifiers', async () => {
  const { ui, component } = await mountCard({
    capacity,
    running: [
      unit({ kind: 'check', id: 'e95ecf25d36c-1793252114266057703-frontend-check', label: 'frontend-check', source: { kind: 'pull-request', number: 924, trial: false, head: 'e95ecf2' } }),
      unit({ kind: 'check', id: 'd5c344a2b19c-1793252239515981475-go-test-shard-1', label: 'go-test-shard-1', source: { kind: 'pull-request', number: 924, trial: true, head: 'd5c344a' } }),
    ],
    waiting: [],
  })
  const text = document.body.textContent ?? ''
  assert.match(text, /frontend-check · PR #924/)
  assert.match(text, /go-test-shard-1 · merge trial/)
  assert.doesNotMatch(text, /e95ecf2|d5c344a/)
  assert.doesNotMatch(text, /1793252114266057703|1793252239515981475/)
  ui.unmount(component)
})

test('running work shows its holds and waiting work shows the queue words in order', async () => {
  const { ui, component } = await mountCard({
    capacity: { ...capacity, diskBlock: 'disk 91% at the 90% limit', resources: [{ name: 'display', used: 1, limit: 1 }] },
    running: [unit({ heavy: true, sessionName: 'builder', roomName: 'fl-rea', resources: ['display'] })],
    waiting: [
      unit({ kind: 'check', id: 'run-1-lint', label: 'run-1-lint', position: 1, reason: 'waiting: 1 of 1 IO-heavy jobs running, position 1 of 2' }),
      unit({ id: 'job_2', label: 'cargo build', position: 2, reason: '' }),
    ],
  })
  const text = document.body.textContent ?? ''
  assert.match(text, /3 of 8 CPUs/)
  assert.match(text, /3\.0 GiB of 16\.0 GiB/)
  assert.match(text, /Blocked: disk 91% at the 90% limit/)
  assert.match(text, /display: 1 of 1 in use/)
  assert.deepEqual(rows().slice(-3), ['make test', 'run-1-lint', 'cargo build'])
  assert.match(text, /builder · fl-rea/)
  assert.match(text, /IO-heavy/)
  assert.match(text, /waiting: 1 of 1 IO-heavy jobs running, position 1 of 2/)
  assert.match(text, /Waiting for the next queue pass/)
  assert.doesNotMatch(text, /Nothing runs on this node|No work waits/)
  ui.unmount(component)
})
