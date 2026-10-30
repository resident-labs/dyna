import test from 'node:test'
import assert from 'node:assert/strict'
import { meterWake, paceOf, resetsIn } from './pace.ts'

const now = Date.parse('2026-10-20T12:00:00Z')
const weekStart = '2026-10-17T12:00:00Z'
const weekReset = '2026-10-24T12:00:00Z'

test('even pace compares the used share with the elapsed share of a reported window', () => {
  const window = { periodStart: weekStart, resetsAt: weekReset, usedPercent: 50 }
  // Three of seven days have passed, so an even spend leaves 57.14 percent.
  const even = paceOf({ ...window, usedPercent: 43 }, now)
  assert.equal(even?.status, 'on')
  assert.ok(Math.abs((even?.expectedRemaining ?? 0) - 57.142) < 0.01)
  assert.equal(paceOf({ ...window, usedPercent: 70 }, now)?.status, 'ahead')
  assert.equal(paceOf({ ...window, usedPercent: 30 }, now)?.status, 'under')
  assert.equal(paceOf({ period: 'week', resetsAt: weekReset, usedPercent: 70 }, now)?.status, 'ahead', 'a week period needs no start time')
  assert.equal(paceOf({ period: 'month', resetsAt: weekReset, usedPercent: 50 }, now), null, 'a month has no fixed length')
  assert.equal(paceOf({ resetsAt: weekReset, usedPercent: 50 }, now), null, 'no reported length, no tick')
  assert.equal(paceOf({ ...window, usedPercent: undefined }, now), null, 'no reading, no tick')
  assert.equal(paceOf(window, Date.parse(weekReset)), null, 'a passed reset has no pace')
})

test('resets in words the time left and passed resets have no text', () => {
  assert.equal(resetsIn('2026-10-20T13:40:00Z', now), '1h 40m')
  assert.equal(resetsIn('2026-10-21T14:00:00Z', now), '1d 2h')
  assert.equal(resetsIn('2026-10-20T12:00:30Z', now), '1m', 'a part minute counts as a minute')
  assert.equal(resetsIn('2026-10-20T11:00:00Z', now), '')
  assert.equal(resetsIn(undefined, now), '')
})

test('a meter wakes at the next minute or the reset, whichever comes first', () => {
  assert.equal(meterWake('2026-10-20T13:40:00Z', now), Date.parse('2026-10-20T12:01:00Z'))
  assert.equal(meterWake('2026-10-20T12:00:30Z', now), Date.parse('2026-10-20T12:00:30Z'))
  assert.ok(Number.isNaN(meterWake('2026-10-20T12:00:30Z', Date.parse('2026-10-20T12:00:30Z'))), 'a passed reset needs no wake')
  assert.ok(Number.isNaN(meterWake(undefined, now)), 'no reset, no wake')
})
