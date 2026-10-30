import test, { mock } from 'node:test'
import assert from 'node:assert/strict'
import { diskTone, freshness, usageTone } from './nodeUsage.ts'

test('usage tones follow the thresholds and the admission floor', () => {
  assert.deepEqual([79, 80, 94, 95].map(usageTone), ['muted', 'warn', 'warn', 'bad'])
  assert.equal(diskTone(19, 20), 'warn')
  assert.equal(diskTone(20, 20), 'muted')
  assert.equal(diskTone(undefined, 20), 'muted')
})

test('freshness names the last successful read after a failed probe', () => {
  mock.timers.enable({ apis: ['Date'], now: Date.parse('2026-10-30T12:12:00Z') })
  try {
    const machine = { local: false, seenAt: '2026-10-30T12:00:00Z', status: 'offline' }
    assert.equal(freshness(machine), 'Last read 12m ago · last probe failed')
    assert.equal(freshness({ ...machine, status: 'online' }), 'Probed 12m ago · every 5 min')
    assert.equal(freshness({ ...machine, local: true }), 'Live · every 5 seconds')
  } finally { mock.timers.reset() }
})
