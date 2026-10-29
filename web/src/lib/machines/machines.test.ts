import test from 'node:test'
import assert from 'node:assert/strict'
import { computersText, diskText, loadText, machinesSub, memoryPct, runtimeText, statusText, summaryText, systemText, versionText, editScope } from './machines.ts'
import type { MachineCapabilities, MachineSummary } from '../api/machine.ts'

const machine = (over: Partial<MachineSummary> = {}): MachineSummary => ({
  id: 'm_1', name: 'this-machine', local: true, enabled: true, status: 'online', allowHostChecks: false, labels: ['ci'], ...over,
})

test('a machine row shows one count for each figure the machine reports', () => {
  assert.equal(summaryText(machine({ agents: 3, computers: 1 })), '3 agents · 1 computer')
  assert.equal(summaryText(machine({ agents: 1 })), '1 agent', 'one agent is not "1 agents"')
  assert.equal(summaryText(machine()), '', 'a machine with no live figure shows no counts')
})

test('a machine shows its queue, and its waiting count once the node queue reports it', () => {
  const ci = machine({ agents: 0, labels: ['ci'] })
  assert.equal(summaryText({ ...ci, ci: { running: 2, waiting: 5 } }), '0 agents · 2 running · 5 waiting')
  assert.equal(summaryText({ ...ci, ci: { running: 1, waiting: 0 } }), '0 agents · 1 running', 'no waiting work hides the waiting count')
  assert.equal(summaryText({ ...ci, enabled: false, status: 'disabled', ci: { running: 0, waiting: 0 } }), '0 agents · CI off', 'a disabled machine takes no CI work')
})

test('a machine without the ci label reports no CI queue', () => {
  const plain = machine({ agents: 0, labels: [] })
  assert.equal(summaryText({ ...plain, ci: { running: 2, waiting: 5 } }), '0 agents · no CI label', 'only the ci label runs local CI checks')
})

test('the header counts machines by status and leaves out what is zero', () => {
  const list = [
    machine(),
    machine({ id: 'm_2', status: 'disabled' }),
    machine({ id: 'm_3', local: false, status: 'offline' }),
    machine({ id: 'm_4', local: false, status: 'unknown' }),
  ]
  assert.equal(machinesSub(list), '1 online · 1 disabled · 1 offline · 1 not probed')
  assert.equal(machinesSub([machine(), machine({ id: 'm_2' })]), '2 online')
  assert.equal(machinesSub([]), 'No machines yet')
  assert.equal(machinesSub(null), '', 'a list that has not loaded yet has no subline')
})

test('status words separate a switched off machine from one that is not probed', () => {
  assert.deepEqual(['online', 'disabled', 'offline', 'unknown'].map((s) => statusText(s as MachineSummary['status'])), ['Online', 'Disabled', 'Offline', 'Not probed'])
})

test('the Disk row words the free space and the rule the node keeps, or the block words', () => {
  const GiB = 2 ** 30
  const TiB = 2 ** 40
  assert.equal(diskText({ freeDisk: 271 * GiB, diskKeeps: '20 GiB and 5% of the disk' }), '271.0 GiB free · keeps 20 GiB and 5% of the disk')
  assert.equal(diskText({ freeDisk: 3 * GiB, diskKeeps: '20 GiB and 5% of the disk', diskBlock: 'disk has 3 GiB free, below the 20 GiB floor' }), '3.0 GiB free · disk has 3 GiB free, below the 20 GiB floor', 'the block words win over what the rule keeps')
  assert.equal(diskText({ freeDisk: 40 * GiB }), '40.0 GiB free', 'no rule words shows only what is free')
  assert.equal(diskText({ freeDisk: 2 * TiB }), '2.0 TiB free')
  assert.equal(diskText({}), '', 'no reading shows no row')
})

test('the page words what the probe read, and leaves out what it could not', () => {
  const caps: MachineCapabilities = {
    os: 'Linux', arch: 'x86_64', cpus: 20, memTotal: 32700000000, memAvailable: 26000000000,
    gpu: true, containers: ['docker'], computers: true, stream: false,
  }
  assert.equal(systemText(caps), 'Linux · x86_64 · 20 CPUs · 30.5 GiB')
  assert.equal(systemText({ ...caps, arch: '', cpus: 0, memTotal: 0 }), 'Linux', 'a probe that read only the OS shows only the OS')
  assert.equal(runtimeText(caps.containers), 'Docker')
  assert.equal(runtimeText(['docker', 'podman']), 'Docker and Podman')
  assert.equal(runtimeText(undefined), 'None')
})

test('the computer capability does not require the desktop stream', () => {
  assert.equal(computersText({ computers: true, stream: false }), 'Yes', 'a machine runs computers without wayvnc')
  assert.equal(computersText({ computers: true, stream: true }), 'Yes, and shows its desktop')
  assert.equal(computersText({ computers: false, stream: true }), 'No', 'a stream without labwc runs no computer')
})

test('a remote machine memory bar shows the used share, and nothing without a reading', () => {
  assert.equal(memoryPct({ memTotal: 100, memAvailable: 25 }), 75)
  assert.equal(memoryPct({ memTotal: 0, memAvailable: 0 }), null, 'no total means no bar')
  assert.equal(loadText({ memAvailable: 2 ** 30 }), '1.0 GiB free')
  assert.equal(loadText({ memAvailable: 0 }), '')
})

test('the Dyna row keeps a version short and leaves the whole string to its title', () => {
  assert.equal(versionText('dyna v0.4.3-0.20261004142815-69873b466650'), 'v0.4.3 (dev)')
  assert.equal(versionText('dyna 0.9.412'), '0.9.412', 'the row already says Dyna')
  assert.equal(versionText('dev+707043d01018'), 'dev+707043d01018', 'a build version shows as it is')
})

test('adding to a CI machine\'s suite list trims, keeps one copy and sorts', () => {
  assert.deepEqual(editScope([], { add: ' unit ' }), ['unit'])
  assert.deepEqual(editScope(['web'], { add: 'unit' }), ['unit', 'web'])
  assert.equal(editScope(['unit'], { add: 'unit' }), null, 'a repeat changes nothing')
  assert.equal(editScope(['unit'], { add: '   ' }), null, 'an empty value changes nothing')
})

test('removing the last room or suite leaves an empty list, which means every one', () => {
  assert.deepEqual(editScope(['a'], { remove: 'a' }), [])
  assert.deepEqual(editScope(['a', 'b'], { remove: 'a' }), ['b'])
  assert.equal(editScope(['a'], { remove: 'zzz' }), null, 'a value the list lacks changes nothing')
})
