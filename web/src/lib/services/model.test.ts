import assert from 'node:assert/strict'
import test from 'node:test'
import { buildRows, type Inputs, elapsed, hhmm, nextAt, queueLine, readableDuration, testLine } from './model.ts'

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

test('the librarian next pass is the engine deferred wake and is empty when none is deferred', () => {
  const hooks = [{ hook: { name: 'librarian-main', label: '', event: 'main-updated', everyMinutes: 0, disabled: false }, lastRun: { id: 'run-1', result: 'pass', startedAt: '2026-10-21T22:49:00Z', finishedAt: '2026-10-21T22:49:36Z' } }]
  const settings = { 'library.enabled': '["/repo"]' }
  assert.equal(row({ hooks, settings, deferred: '2026-10-21T23:19:00Z' }, 'librarian').nextAt, '2026-10-21T23:19:00Z')
  assert.equal(row({ hooks, settings, deferred: null }, 'librarian').nextAt, '')
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
  const hooks = [{ hook: { name: 'self-update', label: '', event: 'schedule', everyMinutes: 60, disabled: false }, lastRun: { id: 'run-1', result: 'pass', startedAt: '2026-10-21T22:49:00Z', finishedAt: '2026-10-21T22:49:36Z' } }]
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
  const lastRun = { id: 'run-1', result: 'pending', startedAt: '2026-10-21T22:49:00Z', finishedAt: '' }
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

test('the watcher and lane sync show their last tick, schedule and error', () => {
  const tick = '2026-10-21T22:52:00Z'
  const changes = [
    { kind: 'github', watching: true, checked: '2026-10-21T22:49:00Z', error: 'old poll failed' },
    { kind: 'github', watching: true, checked: tick, error: 'rate limited' },
    { kind: 'github', watching: false, checked: '2026-10-21T22:58:00Z', error: 'merged change' },
  ]
  const watcher = row({ changes }, 'watcher')
  assert.equal(watcher.state, 'on')
  assert.equal(watcher.tone, 'error')
  // The newest failed poll is the row's error, and the last tick still shows.
  assert.equal(watcher.detail, `rate limited · last tick ${hhmm(tick)}`)
  const clean = changes.slice(0, 2).map((c) => ({ ...c, error: '' }))
  assert.equal(row({ changes: clean }, 'watcher').detail, `Watches 2 open changes each minute · last tick ${hhmm(tick)}`)
  assert.equal(row({ changes: clean }, 'watcher').nextText, 'each minute')
  // The engine sends an unset time as the zero time, which is not a tick.
  assert.equal(row({ changes: [{ kind: 'github', watching: true, checked: '0001-01-01T00:00:00Z' }] }, 'watcher').detail, 'Watches 1 open change each minute · no tick yet')

  const lane = (checkedAt: string, lastError = '') => ({ remotes: [{ checkedAt, lastError }], operationError: '' })
  const laneSync = { '/repo': lane('2026-10-21T22:50:00Z'), '/other': lane('2026-10-21T22:59:00Z', 'other repo failed') }
  assert.equal(row({ laneSync }, 'lanesync').detail, `Fetches 1 lane remote every 5 min · last fetch ${hhmm('2026-10-21T22:50:00Z')}`)
  // A remote error and a failed export both show.
  assert.equal(row({ laneSync: { '/repo': { remotes: [{ checkedAt: '', lastError: 'git fetch failed' }], operationError: 'export failed' } } }, 'lanesync').detail, 'git fetch failed · export failed')
  assert.equal(row({ laneSync: { '/repo': { remotes: [], operationError: 'export failed' } } }, 'lanesync').detail, 'export failed')
  assert.equal(row({ laneSync: { '/repo': lane('0001-01-01T00:00:00Z') } }, 'lanesync').detail, 'Fetches 1 lane remote every 5 min · no fetch yet')
  // A repository the engine does not fetch, and an unreadable status, say so.
  assert.equal(row({ laneSync: { '/other': lane('2026-10-21T22:50:00Z') } }, 'lanesync').detail, 'No lane remotes set up for this repository')
  const unreadable = row({}, 'lanesync')
  assert.equal(unreadable.state, 'unknown')
  assert.equal(unreadable.tone, 'off')
  assert.equal(unreadable.detail, 'Lane status could not be read')
})
