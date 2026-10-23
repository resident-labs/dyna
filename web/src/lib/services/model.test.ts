import assert from 'node:assert/strict'
import test from 'node:test'
import { elapsed, nextAt, queueLine, readableDuration, testLine } from './model.ts'

test('the queue separates trials, ready changes, failures and drafts', () => {
  assert.equal(queueLine(null), 'The queue could not be read')
  assert.equal(queueLine([]), 'No open changes')
  assert.equal(queueLine([
    { state: 'trial-running', reason: '', batch: 1 },
    { state: 'ready', reason: '', batch: 0 },
    { state: 'ci-failed', reason: '', batch: 0 },
    { state: 'held', reason: 'draft', batch: 0 },
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
