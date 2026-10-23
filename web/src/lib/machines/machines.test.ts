import test from 'node:test'
import assert from 'node:assert/strict'
import { machinesSub, summaryText, roleLabel } from './machines.ts'
import type { MachineRole, MachineSummary } from '../api/machine.ts'

const machine = (over: Partial<MachineSummary> = {}): MachineSummary => ({
  id: 'm_1', name: 'this-machine', roles: ['engine', 'agents'], local: true, enabled: true, status: 'online', ...over,
})

test('a machine row shows one count for each figure the machine reports', () => {
  assert.equal(summaryText(machine({ agents: 3, ci: { used: 6, slots: 16 }, computers: 1 })), '3 agents · CI 6/16 slots · 1 computer')
  assert.equal(summaryText(machine({ agents: 1 })), '1 agent', 'one agent is not "1 agents"')
  assert.equal(summaryText(machine()), '', 'a machine with no live figure shows no counts')
})

test('the header counts machines by status and leaves out what is zero', () => {
  const list = [machine(), machine({ id: 'm_2', status: 'off' }), machine({ id: 'm_3', local: false, status: 'unknown' })]
  assert.equal(machinesSub(list), '1 online · 1 off · 1 not probed')
  assert.equal(machinesSub([machine(), machine({ id: 'm_2' })]), '2 online')
  assert.equal(machinesSub([]), 'No machines yet')
  assert.equal(machinesSub(null), '', 'a list that has not loaded yet has no subline')
})

test('role chips name CI in capitals and other roles as they are', () => {
  const roles: MachineRole[] = ['engine', 'agents', 'ci']
  assert.deepEqual(roles.map(roleLabel), ['engine', 'agents', 'CI'])
})
