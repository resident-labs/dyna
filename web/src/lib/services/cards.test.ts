import assert from 'node:assert/strict'
import test from 'node:test'
import { buildCards, readableDuration, type ReviewerInfo, type ServiceInputs } from './cards.ts'

const rev: ReviewerInfo = { enabled: true, profile: 'opus', lastError: '', current: null, queued: 0 }
const setting: Record<string, string> = { 'library.enabled': '[]', 'library.profile': '', 'runs.retention': '720h0m0s', 'worktrees.retention': 'changed' }
const base: ServiceInputs = {
  now: Date.parse('2026-10-20T12:00:00Z'),
  pending: [],
  services: [
    { role: 'reviewer', profile: 'opus', pinned: '', reason: '', profiles: ['opus', 'sonnet'] },
  ],
  reviewer: rev,
  queue: [], history: [], changes: [], journal: [],
  settings: setting,
  usage: [], voice: 'idle', members: [],
}
const card = (inputs: ServiceInputs, id: string) => {
  const found = buildCards(inputs).find((c) => c.id === id)
  assert.ok(found, id)
  return found
}

test('every service has a card, in a fixed order', () => {
  assert.deepEqual(buildCards(base).map((c) => c.id), ['reviewer', 'merge', 'radar', 'librarian', 'maintenance', 'voice'])
})

test('the reviewer card shows running, off and error, with its profile and controls', () => {
  assert.equal(card(base, 'reviewer').tone, 'on')
  assert.equal(card(base, 'reviewer').profile, 'opus')
  const working = card({ ...base, reviewer: { ...rev, current: { pr: 7, title: 'Add x' } } }, 'reviewer')
  assert.deepEqual([working.tone, working.line], ['running', 'Reviewing #7 · Add x'])
  assert.equal(card({ ...base, reviewer: { ...rev, enabled: false } }, 'reviewer').tone, 'paused')
  assert.equal(card({ ...base, reviewer: { ...rev, lastError: 'no profile' } }, 'reviewer').tone, 'error')
  assert.deepEqual(card(base, 'reviewer').controls.map((c) => c.id), ['review.enabled', 'profile.reviewer'])
})

test('the merge card is paused when a queued PR is merge-paused, and shows the last trial', () => {
  const paused = card({ ...base, queue: [{ state: 'merge-paused' }], history: [{ kind: 'trial', outcome: 'failed', at: '2026-10-20T11:00:00Z', changes: 2, detail: '' }] }, 'merge')
  assert.deepEqual([paused.tone, paused.last], ['paused', '2026-10-20T11:00:00Z'])
  assert.match(paused.line, /last trial failed/)
})

test('cost is unknown, not zero, when a turn has no price or the service has no usage role', () => {
  assert.equal(card({ ...base, usage: [{ role: 'reviewer', usd: 1.5, incomplete: false }, { role: 'reviewer', usd: 0.34, incomplete: false }] }, 'reviewer').cost, '$1.84')
  assert.equal(card({ ...base, usage: [{ role: 'reviewer', usd: null, incomplete: false }] }, 'reviewer').cost, 'unknown')
  // one priced turn plus one unpriced turn in the same group: the amount is only a lower bound
  assert.equal(card({ ...base, usage: [{ role: 'reviewer', usd: 1, incomplete: true }] }, 'reviewer').cost, 'unknown')
  assert.equal(card({ ...base, usage: null }, 'reviewer').cost, 'unknown')
  assert.equal(card(base, 'merge').cost, 'unknown')
  assert.equal(card(base, 'reviewer').cost, 'none today')
})

test('a caller that cannot read settings gets no settings controls', () => {
  const cards = buildCards({ ...base, settings: null })
  for (const id of ['librarian', 'maintenance']) assert.deepEqual(cards.find((c) => c.id === id)?.controls, [])
})

test('the librarian is off until a repository is enabled', () => {
  assert.equal(card(base, 'librarian').status, 'off')
  assert.equal(card({ ...base, settings: { ...setting, 'library.enabled': '["/repo"]' } }, 'librarian').status, 'on')
})

test('durations read as people say them', () => {
  assert.equal(readableDuration('336h0m0s'), '14 days')
  assert.equal(readableDuration('24h0m0s'), '1 day')
  assert.equal(readableDuration('36h0m0s'), '36 hours')
  assert.equal(readableDuration('30m0s'), '30 minutes')
  assert.equal(readableDuration('45s'), '45 seconds')
  assert.equal(readableDuration('0s'), 'off')
  assert.equal(readableDuration('off'), 'off')
  assert.equal(readableDuration(''), 'unknown')
})

test('the maintenance card says how long runs are kept in plain words', () => {
  assert.match(card(base, 'maintenance').line, /^Runs kept 30 days · worktrees: changed$/)
})
