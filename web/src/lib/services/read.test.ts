import assert from 'node:assert/strict'
import test from 'node:test'
import { readHistory, readQueue, readReviewer, readServices, readSettings, readUsage, readVoice } from './read.ts'

test('readers keep the engine fields the page uses and drop the rest', () => {
  assert.deepEqual(readServices([{ role: 'keeper', profile: 'sonnet', pinned: '', profiles: ['sonnet'], extra: 1 }]), [{ role: 'keeper', profile: 'sonnet', pinned: '', reason: '', profiles: ['sonnet'] }])
  assert.deepEqual(readReviewer({ enabled: true, profile: 'opus', lastError: '', queue: [{}, {}], reviewers: [{ current: { pr: 9, title: 'T' } }] }), { enabled: true, profile: 'opus', lastError: '', current: { pr: 9, title: 'T' }, queued: 2 })
  assert.deepEqual(readQueue([{ state: 'merge-paused', url: 'u' }]), [{ state: 'merge-paused' }])
  assert.deepEqual(readHistory([{ kind: 'trial', outcome: 'passed', at: '2026-10-20T01:00:00Z', head: 'h', changes: ['a', 'b'], detail: 'go-test' }]), [{ kind: 'trial', outcome: 'passed', at: '2026-10-20T01:00:00Z', changes: 2, detail: 'go-test' }])
  assert.deepEqual(readSettings([{ key: 'runs.retention', value: '720h0m0s' }, { key: 'library.enabled', value: ['/r'] }]), { 'runs.retention': '720h0m0s', 'library.enabled': '["/r"]' })
  assert.deepEqual(readUsage({ byRoleModel: [{ role: 'reviewer', cost: { usd: 1.2, partialTurns: 0, unknownTurns: 0 } }, { role: 'keeper', cost: { usd: null } }, { role: 'librarian', cost: { usd: 1, partialTurns: 1, unknownTurns: 0 } }, { role: 'x', cost: { usd: 2, unknownTurns: 1 } }] }), [{ role: 'reviewer', usd: 1.2, incomplete: false }, { role: 'keeper', usd: null, incomplete: false }, { role: 'librarian', usd: 1, incomplete: true }, { role: 'x', usd: 2, incomplete: true }])
})

test('a failed or malformed result reads as null, never as an empty success', () => {
  assert.equal(readReviewer(null), null)
  assert.equal(readReviewer({ lastError: 'x' }), null)
  assert.equal(readQueue('nope'), null)
  assert.equal(readSettings(undefined), null)
  assert.equal(readUsage([]), null)
  assert.equal(readVoice(undefined), null)
})

test('voice reads live, idle or unavailable', () => {
  assert.equal(readVoice({ available: true, live: true }), 'live')
  assert.equal(readVoice({ available: true, live: false }), 'idle')
  assert.equal(readVoice({ available: false, unavailable: 'No Codex account' }), 'unavailable: No Codex account')
})
