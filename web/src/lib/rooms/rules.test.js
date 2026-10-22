import test from 'node:test'
import assert from 'node:assert/strict'
import { agentRule, globalRule, ruleFilter, ruleUsage, ruleTime, nextRules } from './rules.js'

const owner = { id: 'r1', setter: 'owner', text: 'a' }
const agent = { id: 'r2', setter: 'ses_1', setterName: 'x', text: 'b' }
test('filter splits owner and agent rules', () => {
  assert.equal(agentRule(owner), false)
  assert.equal(agentRule(agent), true)
  assert.deepEqual(ruleFilter([owner, agent]).map((r) => r.id), ['r1', 'r2'])
  assert.deepEqual(ruleFilter([owner, agent], 'owner').map((r) => r.id), ['r1'])
  assert.deepEqual(ruleFilter([owner, agent], 'agents').map((r) => r.id), ['r2'])
})
test('usage groups digits with spaces', () => {
  assert.equal(ruleUsage({ used: 1665, max: 3000 }), '1 665 / 3 000')
  assert.equal(ruleUsage({ used: 0, max: 3000 }), '0 / 3 000')
  assert.equal(ruleUsage(null), '0 / 0')
})
test('time says when a rule was added or last edited', () => {
  const now = Date.parse('2026-10-18T12:00:00Z')
  assert.equal(ruleTime({ createdAt: '2026-10-18T10:00:00Z', updatedAt: '2026-10-18T10:00:00Z' }, now), '2h ago')
  assert.equal(ruleTime({ createdAt: '2026-10-18T10:00:00Z', updatedAt: '2026-10-18T11:55:00Z', edits: [{}] }, now), 'edited 5m ago')
  assert.equal(ruleTime({ createdAt: '2026-10-18T12:00:00Z', updatedAt: '2026-10-18T12:00:00Z' }, now), 'just now')
})
test('a new list replaces the shown one only on a hash change and never during an edit', () => {
  const a = { hash: 'a', rules: [] }, b = { hash: 'b', rules: [] }
  assert.deepEqual(nextRules(a, { hash: 'a' }, false), { shown: a, pending: null })
  assert.deepEqual(nextRules(a, b, false), { shown: b, pending: null })
  assert.deepEqual(nextRules(a, b, true), { shown: a, pending: b })
  assert.deepEqual(nextRules(a, { hash: 'a' }, true), { shown: a, pending: null }, 'a return to the shown list drops the held one')
})

test('global rules start with u', () => {
  assert.equal(globalRule({ id: 'u3' }), true)
  assert.equal(globalRule({ id: 'r3' }), false)
})
