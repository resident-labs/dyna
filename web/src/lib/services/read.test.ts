import assert from 'node:assert/strict'
import test from 'node:test'
import { readHookSummary, readMergeState, readTestHealth, readAccounts, readHistory, readQueue, readReviewer, readServices, readSettings, readUsage, readVoice } from './read.ts'

test('readers keep the engine fields the page uses and drop the rest', () => {
  assert.deepEqual(readServices([{ role: 'librarian', profile: 'sonnet', pinned: '', profiles: ['sonnet'], extra: 1 }]), [{ role: 'librarian', name: '', profile: 'sonnet', pinned: '', reason: '', profiles: ['sonnet'] }])
  assert.deepEqual(readReviewer({ enabled: true, profile: 'opus', lastError: '', queue: [{}, {}], reviewers: [{ current: { pr: 9, title: 'T' } }] }), { enabled: true, profile: 'opus', lastError: '', current: { pr: 9, title: 'T' }, queued: 2, lastVerdictAt: '' })
  assert.deepEqual(readQueue([{ state: 'merge-paused', url: 'u' }]), [{ state: 'merge-paused', reason: '' }])
  assert.deepEqual(readHistory([{ kind: 'trial', outcome: 'passed', at: '2026-10-20T01:00:00Z', head: 'h', changes: ['a', 'b'], detail: 'go-test' }]), [{ kind: 'trial', outcome: 'passed', at: '2026-10-20T01:00:00Z', changes: 2, detail: 'go-test', branch: '', head: 'h', labels: ['a', 'b'] }])
  assert.deepEqual(readSettings([{ key: 'runs.retention', value: '720h0m0s' }, { key: 'library.enabled', value: ['/r'] }]), { 'runs.retention': '720h0m0s', 'library.enabled': '["/r"]' })
  assert.deepEqual(readUsage({ byRoleModel: [{ role: 'reviewer', cost: { usd: 1.2, partialTurns: 0, unknownTurns: 0 } }, { role: 'librarian', cost: { usd: null } }, { role: 'librarian', cost: { usd: 1, partialTurns: 1, unknownTurns: 0 } }, { role: 'x', cost: { usd: 2, unknownTurns: 1 } }] }), [{ role: 'reviewer', turns: 0, usd: 1.2, incomplete: false }, { role: 'librarian', turns: 0, usd: null, incomplete: false }, { role: 'librarian', turns: 0, usd: 1, incomplete: true }, { role: 'x', turns: 0, usd: 2, incomplete: true }])
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

test('hook and merge readers keep pending, disabled, trial identity and an empty paused queue', () => {
  const hook = readHookSummary([{ hook: { name: 'self-update', event: 'main-updated', disabled: true }, lastRun: { result: 'pending', startedAt: '2026-10-21T22:49:00Z' } }])?.[0]
  assert.equal(hook?.hook.disabled, true)
  assert.equal(hook?.lastRun?.result, 'pending')
  assert.equal(hook?.hook.everyMinutes, 0)
  assert.deepEqual(readMergeState({ queue: [], paused: true }), { queue: [], mergePaused: true })
  const queue = readQueue([{ state: 'trial-running', trial: { branch: 'trial/1', batch: ['a', 'b'] } }])
  assert.deepEqual(queue?.[0]?.trial, { branch: 'trial/1', batch: ['a', 'b'] })
  assert.equal(readMergeState({ queue: [], paused: 'false' }), null)
})

test('test health counts only regressions over the limit and account readings keep staleness', () => {
  assert.deepEqual(readTestHealth({ limitMs: 1000, tests: [
    { runs: [{ durationMs: 1100 }, { durationMs: 900 }] },
    { runs: [{ durationMs: 900 }, { durationMs: 800 }] },
    { runs: [{ durationMs: 1100 }, { durationMs: 1200 }] },
    { ignored: 'allowed', runs: [{ durationMs: 1100 }, { durationMs: 900 }] },
    { runs: [{ durationMs: 1100 }] },
  ] }), { count: 1, limitMs: 1000 })
  assert.deepEqual(readAccounts({ accounts: [{ observedAt: '2026-10-21T01:00:00Z', stale: true }] }), { count: 1, at: '2026-10-21T01:00:00Z', stale: true })
  assert.equal(readHookSummary({}), null)
  assert.equal(readTestHealth({}), null)
})
