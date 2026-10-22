import test from 'node:test'
import assert from 'node:assert/strict'
import { remoteState, remoteLabel, remoteList, opRunning, opLine, syncDelay, canExport, popoverPlacement } from './sync.ts'

test('remoteState names each sync state, error first, diverged when both sides moved', () => {
  assert.equal(remoteState({ ahead: 0, behind: 0 }), 'synced')
  assert.equal(remoteState({ ahead: 0, behind: 4 }), 'behind')
  assert.equal(remoteState({ ahead: 3, behind: 0 }), 'ahead')
  assert.equal(remoteState({ ahead: 3, behind: 4 }), 'diverged')
  assert.equal(remoteState({ ahead: 3, behind: 4, rewrite: 'a -> b' }), 'diverged')
  assert.equal(remoteState({ ahead: 0, behind: 0, lastError: 'timeout' }), 'error')
})

test('remoteLabel says in sync, counts, or fetch failed', () => {
  assert.equal(remoteLabel({ ahead: 0, behind: 0 }), 'in sync')
  assert.equal(remoteLabel({ ahead: 3, behind: 4 }), '4 behind · 3 ahead')
  assert.equal(remoteLabel({ ahead: 0, behind: 12 }), '12 behind')
  assert.equal(remoteLabel({ ahead: 2, behind: 0 }), '2 ahead')
  assert.equal(remoteLabel({ ahead: 0, behind: 0, lastError: 'x' }), 'fetch failed')
  assert.equal(remoteLabel({ ahead: 0, behind: 0, rewrite: 'a -> b' }), 'history rewritten')
})

test('remoteList puts origin first and marks it canonical; a missing repo gives no remotes', () => {
  const list = remoteList({ upstream: { ahead: 0, behind: 1 }, origin: { ahead: 0, behind: 0 }, backup: { ahead: 0, behind: 0 } })
  assert.deepEqual(list.map((r) => [r.name, r.canonical]), [['origin', true], ['backup', false], ['upstream', false]])
  assert.deepEqual(remoteList(undefined), [])
})

const op = (state, kind = 'export') => ({ kind, state, startedAt: new Date(Date.now() - 120000).toISOString() })

test('opRunning is true while building, testing or publishing', () => {
  for (const s of ['building', 'testing', 'publishing']) assert.equal(opRunning(op(s)), true)
  for (const s of ['done', 'failed', 'conflict']) assert.equal(opRunning(op(s)), false)
  assert.equal(opRunning(undefined), false)
})

test('opLine says what the operation is doing, and how it ended', () => {
  assert.equal(opLine(op('testing')), 'Export is running, started 2m ago')
  assert.equal(opLine(op('building', 'refresh')), 'Refresh is running, started 2m ago')
  assert.equal(opLine({ ...op('failed'), error: 'CI failed in web test' }), 'Export failed: CI failed in web test')
  assert.equal(opLine(op('conflict', 'refresh')), 'Refresh stopped on a conflict')
  assert.equal(opLine({ ...op('done'), prURL: 'https://x/pr/1' }), 'Export done')
  assert.equal(opLine(undefined), '')
})

test('syncDelay polls fast while an operation runs and slowly otherwise', () => {
  assert.equal(syncDelay(op('testing')), 5000)
  assert.equal(syncDelay(op('done')), 60000)
  assert.equal(syncDelay(undefined), 60000)
})

test('canExport needs integration commits and no running operation', () => {
  const commits = [{ sha: 'a' }]
  assert.equal(canExport({ integration: { commits } }), true)
  assert.equal(canExport({ integration: { commits: [] } }), false)
  assert.equal(canExport({ integration: { commits }, operation: op('testing') }), false)
  assert.equal(canExport(undefined), false)
})

test('opLine shows publication attempts and the last error', () => {
  assert.equal(opLine({ ...op('publishing'), attempts: 2, error: 'push rejected' }), 'Export is running, started 2m ago · attempt 2 of 5, last error: push rejected')
  assert.equal(opLine({ ...op('publishing'), attempts: 1 }), 'Export is running, started 2m ago')
  assert.equal(opLine({ ...op('publishing'), attempts: 1, error: 'push rejected' }), 'Export is running, started 2m ago · last error: push rejected')
  assert.equal(opLine({ ...op('failed'), attempts: 5, error: 'push rejected' }), 'Export failed after 5 attempts: push rejected')
  assert.equal(opLine({ ...op('failed'), attempts: 1, error: 'lease lost' }), 'Export failed: lease lost')
})

test('the popover stays inside a narrow viewport instead of clipping past the left edge', () => {
  // A 390 px phone: a 250 px badge leaves no room for 300 px to its left.
  const place = popoverPlacement(20, 250, 390)
  assert.equal(place.width, 300)
  assert.equal(place.left, 8 - 20)
  assert.ok(20 + place.left >= 8)
  assert.ok(20 + place.left + place.width <= 390 - 8)
  // A badge near the right edge keeps its usual right alignment.
  assert.deepEqual(popoverPlacement(1000, 1120, 1440), { left: 1120 - 300 - 1000, width: 300 })
  // A viewport narrower than the panel shrinks it and keeps both gutters.
  assert.deepEqual(popoverPlacement(0, 120, 280), { left: 8, width: 264 })
})
