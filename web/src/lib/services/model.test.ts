import assert from 'node:assert/strict'
import test from 'node:test'
import { buildRows, type Inputs, elapsed, nextAt, queueLine, readableDuration, testLine } from './model.ts'

test('the queue separates trials, ready changes, failures and drafts', () => {
  assert.equal(queueLine(null), 'The queue could not be read')
  assert.equal(queueLine([]), 'No open changes')
  assert.equal(queueLine([
    { state: 'trial-running', reason: '', trial: { branch: 'dyna/merge-trial/1', batch: ['a', 'b'] } },
    { state: 'trial-running', reason: '', trial: { branch: 'dyna/merge-trial/1', batch: ['a', 'b'] } },
    { state: 'ready', reason: '' },
    { state: 'ci-failed', reason: '' },
    { state: 'held', reason: 'draft' },
  ]), '1 trial running · 1 ready · 1 CI failed · 1 held by drafts')
})

test('only enabled schedule hooks with a valid run have a next time', () => {
  const hook = { name: 'self-update', label: '', event: 'schedule', everyMinutes: 60, disabled: false }
  const lastRun = { result: 'pass', startedAt: '2026-10-21T22:49:00Z', finishedAt: '2026-10-21T22:49:36Z' }
  assert.equal(nextAt({ hook, lastRun }), '2026-10-21T23:49:00.000Z')
  assert.equal(nextAt({ hook: { ...hook, disabled: true }, lastRun }), '')
  assert.equal(nextAt({ hook: { ...hook, event: 'pr-merged' }, lastRun }), '')
  assert.equal(nextAt({ hook, lastRun: { ...lastRun, startedAt: 'bad date' } }), '')
  assert.equal(nextAt({ hook, lastRun: null }), '')
})

test('durations keep zero retention off and test health hides at zero', () => {
  assert.equal(readableDuration('336h0m0s'), '14 days')
  assert.equal(readableDuration('0s'), 'off')
  assert.equal(elapsed('2026-10-21T22:49:00Z', '2026-10-21T22:49:36Z'), '36 s')
  assert.equal(elapsed('bad date', ''), '')
  assert.equal(testLine({ count: 3, limitMs: 1000 }), '3 tests over 1 s')
  assert.equal(testLine({ count: 0, limitMs: 1000 }), '')
})

const inputs = (over: Partial<Inputs> = {}): Inputs => ({
  reviewer: null, queue: null, mergePaused: null, history: [], services: null, notices: null,
  hooks: null, runs: {}, settings: null, budget: null, accounts: null, usage: null, voice: null,
  changes: [], testHealth: null, repo: '/repo', ...over,
})
const row = (over: Partial<Inputs>, id: string) => {
  const found = buildRows(inputs(over)).find((r) => r.id === id)
  assert.ok(found, `missing ${id}`)
  return found
}

test('an empty merge queue still shows its stored pause state', () => {
  const paused = row({ queue: [], mergePaused: true }, 'merge')
  assert.equal(paused.state, 'paused')
  assert.equal(paused.toggle?.on, false)
  assert.equal(row({ queue: [], mergePaused: false }, 'merge').toggle?.on, true)
  assert.equal(row({ queue: [] }, 'merge').toggle, null)
})

test('librarian status belongs to this repository and keeps activation local', () => {
  const settings = { 'library.enabled': '["/other"]', 'library.profile': 'sol' }
  const off = row({ settings }, 'librarian')
  assert.equal(off.state, 'off')
  assert.equal(off.toggle, null)
  assert.equal(off.detail, 'Turn on with dyna library enable on this machine')
  assert.equal(row({ settings: { ...settings, 'library.enabled': '["/repo"]' } }, 'librarian').state, 'on')
  assert.equal(row({ settings: { 'library.enabled': 'bad JSON' } }, 'librarian').state, 'unknown')
})

test('profiles preserve Auto and partial cost falls back to turns', () => {
  const services = [{ role: 'reviewer', name: '', profile: 'sol', pinned: '', reason: '', profiles: ['sol'] }]
  assert.equal(row({ services }, 'reviewer').profile?.value, '')
  assert.deepEqual(row({ services }, 'reviewer').profile?.options, ['', 'sol'])
  const priced = { role: 'reviewer', turns: 4, usd: 1.2, incomplete: false }
  const usage = [{ ...priced, incomplete: true }]
  assert.equal(row({ usage }, 'reviewer').value, '4 turns')
  assert.equal(row({ usage: [priced] }, 'reviewer').value, '$1.20')
})

test('the radar counts each overlapping change once', () => {
  const hooks = [{ hook: { name: 'self-update', label: '', event: 'schedule', everyMinutes: 60, disabled: false }, lastRun: { result: 'pass', startedAt: '2026-10-21T22:49:00Z', finishedAt: '2026-10-21T22:49:36Z' } }]
  const rows = buildRows(inputs({ hooks, changes: [{ overlaps: 2 }, { overlaps: 1 }] }))
  // The count is already in the line, so the row carries no second number.
  assert.equal(rows.find((r) => r.id === 'radar')?.detail, '2 open changes overlap another')
  assert.ok(!rows.some((r) => r.id === 'keeper'))
})

test('the built-in update hook names its main-updated trigger', () => {
  const hook = { name: 'self-update', label: 'Update Dyna', event: 'main-updated', everyMinutes: 0, disabled: false }
  const hooks = [{ hook, lastRun: null }]
  const update = row({ hooks }, 'update')
  assert.equal(update.detail, 'When main updates · no run yet')
  assert.equal(update.nextAt, '')
  assert.equal(row({ hooks: [{ hook: { ...hook, disabled: true }, lastRun: null }] }, 'update').nextText, '')
})

test('a pending update reports running until it has a real result', () => {
  const hook = { name: 'self-update', label: 'Update Dyna', event: 'main-updated', everyMinutes: 0, disabled: false }
  const lastRun = { result: 'pending', startedAt: '2026-10-21T22:49:00Z', finishedAt: '' }
  const update = row({ hooks: [{ hook, lastRun }] }, 'update')
  assert.equal(update.state, 'running')
  assert.equal(update.tone, 'live')
  assert.equal(update.detail, 'When main updates · running')
  assert.equal(update.value, '')
  assert.equal(row({ hooks: [{ hook, lastRun: { ...lastRun, result: 'pass' } }] }, 'update').detail, 'When main updates · last passed')
})

test('a busy reviewer names a provider limit once, and an idle one ignores it', () => {
  const services = [{ role: 'reviewer', name: '', profile: 'sonnet', pinned: '', reason: '', profiles: [] }]
  const notices = [{ key: 'provider:sonnet', severity: 'warn', text: 'sonnet: provider limit reached' }]
  const idle = { enabled: true, profile: 'sonnet', lastError: '', current: null, queued: 0, lastVerdictAt: '' }
  assert.equal(row({ services, notices, reviewer: idle }, 'reviewer').state, 'idle')
  assert.equal(row({ services, notices, reviewer: { ...idle, queued: 2 } }, 'reviewer').state, 'rate-limited')
})
