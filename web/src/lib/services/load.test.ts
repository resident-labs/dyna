import assert from 'node:assert/strict'
import test from 'node:test'
import { emptyData, loadServiceData, type ServiceApi } from './load.ts'

const ok = (v: unknown) => () => Promise.resolve(v)
const api: ServiceApi = {
  listServices: ok([{ role: 'keeper', profile: 'sonnet', pinned: '', reason: '', profiles: [] }]),
  reviewerStatus: ok({ enabled: true, profile: 'opus', lastError: '', queue: [], reviewers: [] }),
  roomMergeStatus: ok([]),
  roomMergeHistory: ok([]),
  settings: () => Promise.reject(new Error('needs admin')),
  usageRollup: ok({ byRoleModel: [] }),
  voiceStatus: ok({ available: true, live: false }),
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
  assert.equal(emptyData.pending.length, 7)
  let release: () => void = () => {}
  const slow = new Promise<unknown>((resolve) => { release = () => resolve([]) })
  const seen: string[][] = []
  const done = loadServiceData({ ...api, roomMergeStatus: () => slow }, 'room', new Date('2026-10-20T12:00:00Z'), emptyData, (d) => seen.push([...d.pending]))
  await new Promise((r) => setTimeout(r, 0))
  assert.ok(seen.length > 0 && seen.every((p) => p.includes('queue')), 'the slow queue holds back only its own card')
  release()
  assert.deepEqual((await done).pending, [])
})

test('a call that never answers times out and leaves its part empty', async () => {
  const data = await loadServiceData({ ...api, voiceStatus: () => new Promise(() => {}) }, 'room', new Date('2026-10-20T12:00:00Z'), emptyData, () => {}, 20)
  assert.deepEqual(data.pending, [])
  assert.equal(data.voice, null)
  assert.equal(data.reviewer?.enabled, true)
})
