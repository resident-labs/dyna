import { test } from 'node:test'
import assert from 'node:assert/strict'
import { resolveAgent } from './agentRef.ts'
import type { AgentRef } from './agentRef.ts'

const usageRow: AgentRef = { id: 'lead', name: 'lead', hue: 0, roomId: 'room-1' }

test('a usage row opens the roster member, which names its computer', () => {
  const member = { id: 'lead', name: 'lead', computer: 'lead' }
  assert.equal(resolveAgent(usageRow, [{ id: 'other', name: 'other', computer: 'other' }, member]), member)
})

test('a usage row for a session outside the roster names its computer by session ID', () => {
  const got = resolveAgent(usageRow, [])
  assert.equal('computer' in got && got.computer, 'lead')
})

test('a reference without a session ID has no computer to name', () => {
  const got = resolveAgent({ name: 'agent', hue: 0 }, [{ id: 'lead', name: 'lead', computer: 'lead' }])
  assert.equal('computer' in got && got.computer, undefined)
})
