import assert from 'node:assert/strict'
import test from 'node:test'
import { emptyData, loadServiceData, type ServiceApi } from './load.ts'

const ok = (v: unknown) => () => Promise.resolve(v)
const api: ServiceApi = {
  listServices: ok([{ role: 'reviewer', profile: 'sonnet', pinned: '', reason: '', profiles: [] }]),
  reviewerStatus: ok({ enabled: true, profile: 'opus', lastError: '', queue: [], reviewers: [] }),
  mergeControlStatus: ok({ queue: [], paused: false }),
  roomMergeHistory: ok([]),
  settings: () => Promise.reject(new Error('needs admin')),
  usageRollup: ok({ byRoleModel: [] }),
  voiceStatus: ok({ available: true, live: false }),
  notices: ok([]),
  roomCIBudget: ok({ ciWarnAfter: '2m', ciFailAfter: '3m', ciTestMax: '1s' }),
  accountsList: ok({ providers: [] }),
  ciHealth: ok({ limitMs: 1000, tests: [] }),
  syncStatus: ok({}),
}

test('one failing call leaves its part empty and the others loaded', async () => {
  const data = await loadServiceData(api, 'room', new Date('2026-10-20T12:00:00Z'))
  assert.deepEqual(data.pending, [])
  assert.equal(data.settings, null)
  assert.equal(data.services.length, 1)
  assert.equal(data.reviewer?.enabled, true)
  assert.equal(data.voice, 'idle')
})

test('usage is asked for today, from the start of the UTC day to now', async () => {
  const seen: string[] = []
  await loadServiceData({ ...api, usageRollup: (_room, from, to) => { seen.push(from, to); return Promise.resolve({ byRoleModel: [] }) } }, 'room', new Date('2026-10-20T12:34:00Z'))
  assert.deepEqual(seen, ['2026-10-20T00:00:00.000Z', '2026-10-20T12:34:00.000Z'])
})

test('a call that throws at once counts as failed, not as a crash', async () => {
  const data = await loadServiceData({ ...api, reviewerStatus: () => { throw new Error('missing') } }, 'room', new Date('2026-10-20T12:00:00Z'))
  assert.equal(data.reviewer, null)
  assert.equal(data.voice, 'idle')
})

test('a failed call keeps what was loaded before', async () => {
  const first = await loadServiceData(api, 'room', new Date('2026-10-20T12:00:00Z'))
  const next = await loadServiceData({ ...api, reviewerStatus: () => Promise.reject(new Error('busy')) }, 'room', new Date('2026-10-20T12:01:00Z'), first)
  assert.equal(next.reviewer?.enabled, true)
  assert.equal(next.voice, 'idle')
})

test('every part is pending until its call answers, and each part arrives on its own', async () => {
  assert.equal(emptyData.pending.length, 13)
  let release: () => void = () => {}
  const slow = new Promise<unknown>((resolve) => { release = () => resolve({ queue: [], paused: false }) })
  const seen: string[][] = []
  let arrived: () => void = () => {}
  const first = new Promise<void>((resolve) => { arrived = resolve })
  const done = loadServiceData({ ...api, mergeControlStatus: () => slow }, 'room', new Date('2026-10-20T12:00:00Z'), emptyData, (d) => { seen.push([...d.pending]); arrived() })
  await first
  assert.ok(seen.length > 0 && seen.every((p) => p.includes('queue')), 'the slow queue holds back only its own card')
  release()
  assert.deepEqual((await done).pending, [])
})

test('a call that never answers times out and leaves its part empty', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout'] })
  let ready: () => void = () => {}
  const settled = new Promise<void>((resolve) => { ready = resolve })
  const pending = loadServiceData({ ...api, voiceStatus: () => new Promise(() => {}) }, 'room', new Date('2026-10-20T12:00:00Z'), emptyData, (d) => {
    if (d.pending.length === 1 && d.pending[0] === 'voice') ready()
  }, 20)
  await settled
  t.mock.timers.tick(20)
  const data = await pending
  assert.deepEqual(data.pending, [])
  assert.equal(data.voice, null)
  assert.equal(data.reviewer?.enabled, true)
})

test('hook reads settle independently and a failed refresh keeps their last result', async () => {
  const now = new Date('2026-10-20T12:00:00Z')
  const first = await loadServiceData(api, 'room', now, emptyData, () => {}, 20000,
    () => Promise.resolve([{ hook: { name: 'self-update', event: 'main-updated', disabled: true } }]))
  const next = await loadServiceData({ ...api, notices: () => Promise.reject(new Error('unavailable')) }, 'room', now, first, () => {}, 20000,
    () => Promise.reject(new Error('unavailable')))
  assert.equal(next.hooks?.[0]?.hook.disabled, true)
  assert.deepEqual(next.notices, [])
  assert.equal(next.budget?.testMax, '1s')
  assert.deepEqual(next.pending, [])
  assert.equal(next.mergePaused, false)
})
