import assert from 'node:assert/strict'
import test from 'node:test'
import type { RegistryService } from '../api/service.ts'
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
  const lastRun = { id: 'run-1', result: 'pass', startedAt: '2026-10-21T22:49:00Z', finishedAt: '2026-10-21T22:49:36Z' }
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
  reviewer: null, queue: null, mergePaused: null, history: [], registry: null, services: null, notices: null,
  hooks: null, runs: {}, reasons: {}, settings: null, budget: null, accounts: null, usage: null, voice: null,
  changes: [], testHealth: null, laneSync: null, deferred: null, repo: '/repo', ...over,
})
const row = (over: Partial<Inputs>, id: string) => {
  const found = buildRows(inputs(over)).find((r) => r.id === id)
  assert.ok(found, `missing ${id}`)
  return found
}

test('an empty merge queue still shows its stored pause state', () => {
  const paused = row({ queue: [], mergePaused: true }, 'merge')
  assert.equal(paused.toggle?.on, false)
  assert.equal(row({ queue: [], mergePaused: false }, 'merge').toggle?.on, true)
  assert.equal(row({ queue: [] }, 'merge').toggle, null)
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
  const hooks = [{ hook: { name: 'self-update', label: '', event: 'schedule', everyMinutes: 60, disabled: false }, lastRun: { id: 'run-1', result: 'pass', startedAt: '2026-10-21T22:49:00Z', finishedAt: '2026-10-21T22:49:36Z' } }]
  const rows = buildRows(inputs({ hooks, changes: [{ overlaps: 2 }, { overlaps: 1 }] }))
  // The count is already in the line, so the row carries no second number.
  assert.equal(rows.find((r) => r.id === 'radar')?.detail, '2 open changes overlap another')
  assert.ok(!rows.some((r) => r.id === 'keeper'))
})

// The page takes each service's state, detail and times from the engine's
// registry, the list `dyna services` prints, so the two cannot disagree.
test('a row reads its state, detail and times from the registry', () => {
  const registry: RegistryService[] = [
    { id: 'updates', group: 'this-computer', label: 'Updates', description: 'Keeps Dyna current', state: 'on', detail: '', count: 0, lastRun: '2026-10-21T22:49:00Z', nextRun: '2026-10-21T23:49:00Z', profile: '', session: '', settingsKeys: [] },
    { id: 'merge-queue', group: 'pull-requests', label: 'Merge queue', description: '', state: 'running', detail: 'working', count: 2, profile: '', session: '', settingsKeys: [] },
  ]
  const update = row({ registry }, 'update')
  assert.deepEqual([update.state, update.tone, update.detail, update.last, update.nextAt], ['on', 'on', 'Keeps Dyna current', '2026-10-21T22:49:00Z', '2026-10-21T23:49:00Z'])
  const merge = row({ registry }, 'merge')
  assert.deepEqual([merge.state, merge.tone, merge.detail], ['running', 'live', 'Working · 2 waiting'])
  assert.equal(row({ registry }, 'voice').state, 'unknown')
  assert.equal(row({ registry: null }, 'update').detail, 'The service list could not be read')
})
