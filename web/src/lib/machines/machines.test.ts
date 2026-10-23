import test from 'node:test'
import assert from 'node:assert/strict'
import { machinesSub, summaryText, roleLabel, statusText } from './machines.ts'
import type { MachineRole, MachineSummary } from '../api/machine.ts'

const machine = (over: Partial<MachineSummary> = {}): MachineSummary => ({
  id: 'm_1', name: 'this-machine', roles: ['engine', 'agents'], local: true, enabled: true, status: 'online', ciSlots: 0, ...over,
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

test('role chips name CI in capitals and other roles as they are', () => {
  const roles: MachineRole[] = ['engine', 'agents', 'ci']
  assert.deepEqual(roles.map(roleLabel), ['engine', 'agents', 'CI'])
})
