import test, { mock } from 'node:test'
import assert from 'node:assert/strict'
import { diskTone, freshness, usageTone } from './nodeUsage.ts'
import { createTestServer } from '../../../test/ssr-server.ts'

test('usage tones follow the thresholds and the admission floor', () => {
  assert.deepEqual([79, 80, 94, 95].map(usageTone), ['neutral', 'warning', 'warning', 'danger'])
  assert.equal(diskTone(19, 20), 'warning')
  assert.equal(diskTone(20, 20), 'neutral')
  assert.equal(diskTone(undefined, 20), 'neutral')
})

test('local disk names its probe time separately from live CPU and memory', async (t) => {
  mock.timers.enable({ apis: ['Date'], now: Date.parse('2026-10-30T12:12:00Z') })
  try {
    const vite = await createTestServer(t)
    const { render } = await vite.ssrLoadModule('svelte/server')
    const { default: Card } = await vite.ssrLoadModule('/src/lib/machines/NodeUsageCard.svelte')
    const machine = { local: true, status: 'online', seenAt: '2026-10-30T12:00:00Z', capabilities: { freeDisk: 100 * 2 ** 30, diskFloor: 20 * 2 ** 30 } }
    const body = render(Card, { props: { machine, load: null, work: null } }).body
    assert.match(body, /CPU and memory live · every 5 seconds/)
    assert.match(body, /floor 20\.0 GiB · probed 12m ago/)
  } finally { mock.timers.reset() }
})

test('freshness names the last successful read after a failed probe', () => {
  mock.timers.enable({ apis: ['Date'], now: Date.parse('2026-10-30T12:12:00Z') })
  try {
    const machine = { local: false, seenAt: '2026-10-30T12:00:00Z', status: 'offline' }
    assert.equal(freshness(machine), 'Last read 12m ago · last probe failed')
    assert.equal(freshness({ ...machine, status: 'online' }), 'Probed 12m ago · every 5 min')
    assert.equal(freshness({ ...machine, local: true }), 'CPU and memory live · every 5 seconds')
  } finally { mock.timers.reset() }
})
