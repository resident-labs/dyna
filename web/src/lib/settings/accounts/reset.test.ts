import test from 'node:test'
import assert from 'node:assert/strict'
import { resetNotice, quotaBlock, blockUntil } from './reset.ts'

const now = Date.parse('2026-10-20T12:00:00Z')
const observedAt = '2026-10-20T10:00:00Z'

test('passed resets hide old readings and state when they were checked', () => {
  const notice = resetNotice({ resetsAt: '2026-10-20T11:00:00Z' }, observedAt, now)
  assert.match(notice, /^probably reset · last checked /)
  assert.doesNotMatch(notice, /%/)
  assert.equal(resetNotice({ resetsAt: '2026-10-20T12:00:00Z' }, observedAt, now).startsWith('probably reset'), true)
  assert.match(resetNotice({ unavailableReason: 'probably reset' }, observedAt, now), /^probably reset · last checked /)
  assert.equal(resetNotice({ resetsAt: '2026-10-20T12:00:00Z' }, '', now), 'probably reset · last checked unknown')
})

test('fresh readings and unknown reset times keep their reported values', () => {
  for (const resetsAt of ['2026-10-20T10:00:00Z', '2026-10-20T09:00:00Z', '2026-10-20T13:00:00Z', 'invalid', '']) {
    assert.equal(resetNotice({ resetsAt }, observedAt, now), '')
  }
})

// The host's blocking decision, computed against the clock like service.ApplyBlockingQuota.
test('a block lasts until its last exhausted window resets', () => {
  const windows = [
    { id: 'five_hour', usedPercent: 100, resetsAt: '2026-10-20T13:00:00Z' },
    { id: 'seven_day', used: 10, limit: 10, resetsAt: '2026-10-20T15:00:00Z' },
    { id: 'other', usedPercent: 100, resetsAt: '2026-10-20T18:00:00Z' },
  ]
  const block = quotaBlock(windows, 'codex', now)
  assert.deepEqual(block?.ids, ['five_hour', 'seven_day'], 'only the windows that stop the account count')
  assert.equal(block?.until, Date.parse('2026-10-20T15:00:00Z'))
  assert.equal(quotaBlock(windows, 'codex', Date.parse('2026-10-20T15:00:00Z')), null, 'a reset exactly at the deadline frees the account')
  assert.equal(blockUntil({ windows, harness: 'codex' }, now), Date.parse('2026-10-20T15:00:00Z'))
  assert.equal(blockUntil({ windows, harness: 'codex' }, Date.parse('2026-10-20T16:00:00Z')), Number.NaN, 'a deadline behind the clock asks for no wake')
})

test('a partly used window or an unknown reset never blocks', () => {
  assert.equal(quotaBlock([{ id: 'five_hour', usedPercent: 40, resetsAt: '2026-10-20T15:00:00Z' }], 'codex', now), null)
  assert.equal(quotaBlock([{ id: 'five_hour', usedPercent: 100 }], 'codex', now), null)
  assert.equal(quotaBlock([{ id: 'five_hour', usedPercent: 100, resetsAt: 'invalid' }], 'codex', now), null)
  assert.equal(quotaBlock([], 'codex', now), null)
  assert.equal(quotaBlock(undefined, 'codex', now), null)
  assert.equal(Number.isNaN(blockUntil({ windows: [] })), true, 'no deadline means no wake')
})

test('a reading without windows keeps the host decision until its deadline', () => {
  const until = '2026-10-20T15:00:00Z'
  assert.equal(blockUntil({ quotaBlocked: true, quotaBlockedUntil: until }, now), Date.parse(until))
  assert.equal(blockUntil({ quotaBlocked: true, quotaBlockedUntil: until }, Date.parse(until)), Number.NaN, 'expired at the deadline')
  assert.equal(blockUntil({ quotaBlocked: true, quotaBlockedUntil: until }, Date.parse('2026-10-20T16:00:00Z')), Number.NaN, 'expired after it')
  assert.equal(blockUntil({ quotaBlocked: false, quotaBlockedUntil: until }, now), Number.NaN)
  assert.equal(blockUntil({ quotaBlocked: true }, now), Number.NaN, 'no deadline to wake at')
  assert.equal(blockUntil(null), Number.NaN)
  assert.equal(blockUntil({ quotaBlocked: true, quotaBlockedUntil: until, windows: [] }, now), Date.parse(until))
})
