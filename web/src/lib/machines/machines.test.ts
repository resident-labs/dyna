import test from 'node:test'
import assert from 'node:assert/strict'
import { computersText, diskText, loadText, machinesSub, memoryPct, reasonText, roleLabel, runtimeText, statusText, summaryText, systemText, versionText, editScope } from './machines.ts'
import type { MachineCapabilities, MachineRole, MachineSummary } from '../api/machine.ts'

const machine = (over: Partial<MachineSummary> = {}): MachineSummary => ({
  id: 'm_1', name: 'this-machine', roles: ['engine', 'agents'], local: true, enabled: true, status: 'online', ciSlots: 0, allowHostChecks: false, ...over,
})

test('a machine row shows one count for each figure the machine reports', () => {
  assert.equal(summaryText(machine({ agents: 3, computers: 1 })), '3 agents · 1 computer')
  assert.equal(summaryText(machine({ agents: 1 })), '1 agent', 'one agent is not "1 agents"')
  assert.equal(summaryText(machine()), '', 'a machine with no live figure shows no counts')
})

test('a CI machine shows its slots, and its use once the CI work reports it', () => {
  const ci = machine({ roles: ['ci'], ciSlots: 16, agents: 0 })
  assert.equal(summaryText(ci), '0 agents · CI 16 slots')
  assert.equal(summaryText({ ...ci, ci: { used: 6, slots: 16 } }), '0 agents · CI 6/16 slots')
  assert.equal(summaryText({ ...ci, ciSlots: 1, ci: { used: 1, slots: 1 } }), '0 agents · CI 1/1 slot', 'the noun follows the total')
  assert.equal(summaryText({ ...ci, enabled: false, status: 'disabled', ciSlots: 2, ci: { used: 0, slots: 2 } }), '0 agents · CI off', 'a disabled machine takes no CI work')
})

test('the header counts machines by status and leaves out what is zero', () => {
  const list = [
    machine(),
    machine({ id: 'm_5', status: 'degraded' }),
    machine({ id: 'm_2', status: 'disabled' }),
    machine({ id: 'm_3', local: false, status: 'offline' }),
    machine({ id: 'm_4', local: false, status: 'unknown' }),
  ]
  assert.equal(machinesSub(list), '1 online · 1 degraded · 1 disabled · 1 offline · 1 not probed')
  assert.equal(machinesSub([machine(), machine({ id: 'm_2' })]), '2 online')
  assert.equal(machinesSub([]), 'No machines yet')
  assert.equal(machinesSub(null), '', 'a list that has not loaded yet has no subline')
})

test('status words separate a switched off machine from one that is not probed', () => {
  assert.deepEqual(['online', 'degraded', 'disabled', 'offline', 'unknown'].map((s) => statusText(s as MachineSummary['status'])), ['Online', 'Degraded', 'Disabled', 'Offline', 'Not probed'])
})

test('a degraded machine says why in a designed line', () => {
  assert.equal(reasonText('low-disk'), 'Low disk space. Dyna starts no new CI here until space is freed.')
  assert.equal(reasonText(undefined), 'The machine still works, but it needs attention.', 'an unknown reason still reads as a sentence')
})

test('the Disk row words the free space and the floor Dyna keeps', () => {
  const GiB = 2 ** 30
  const TiB = 2 ** 40
  assert.equal(diskText({ freeDisk: 3 * GiB, diskFloor: 20 * GiB }), '3.0 GiB free · needs 20.0 GiB')
  assert.equal(diskText({ freeDisk: 40 * GiB }), '40.0 GiB free', 'no floor shows only what is free')
  assert.equal(diskText({ freeDisk: 1000 * GiB, diskFloor: 2000 * GiB }), '1000.0 GiB free · needs 2.0 TiB', 'a large floor steps up to the next unit')
  assert.equal(diskText({ freeDisk: 2 * TiB }), '2.0 TiB free')
  assert.equal(diskText({}), '', 'no reading shows no row')
})

test('role chips name CI in capitals and other roles as they are', () => {
  const roles: MachineRole[] = ['engine', 'agents', 'ci']
  assert.deepEqual(roles.map(roleLabel), ['engine', 'agents', 'CI'])
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

test('the Machine page shows Rooms and Suites only for a CI machine, and read-only unless the caller is the owner', async () => {
  const { readFileSync } = await import('node:fs')
  const src = readFileSync(new URL('./MachinePage.svelte', import.meta.url), 'utf8')
  const ci = src.slice(src.indexOf("{#if machine.roles.includes('ci')}\n          <Row dense text=\"Rooms\">"))
  assert.ok(ci.startsWith("{#if machine.roles.includes('ci')}"), 'the Rooms row sits inside the CI-role block')
  assert.ok(ci.indexOf('text="Suites"') < ci.indexOf('text="Host checks"'), 'Suites sits in the same block, before host checks')
  assert.equal(src.match(/readOnly=\{!owner\}/g)?.length, 2, 'both lists go read-only for a caller who is not the owner')
})
