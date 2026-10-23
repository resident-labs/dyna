import test from 'node:test'
import assert from 'node:assert/strict'
import { prView, chipOf } from './prCard.ts'
import type { PRPostInput } from './prCard.ts'
import type { PREvent } from '../rooms/events.gen.ts'

const HEAD = 'a'.repeat(40)
const CHANGE = 'github.com/acme/app#542'

// post is a typed PR post of one event.
function post(ev: Partial<PREvent>, id = 'msg_0123456789abcdef', ts = 1): PRPostInput {
  const payload: PREvent = { type: 'update', label: 'PR #542', location: 'https://github.com/acme/app/pull/542', changeId: CHANGE, number: 542, repo: 'acme/app', head: HEAD, url: 'https://github.com/acme/app/pull/542', ...ev }
  return { id, ts, kind: 'pr', notice: { kind: payload.type }, payload, text: 'agent text' }
}

test('a typed PR post draws its facts from the payload, not its text', () => {
  const view = prView([post({ type: 'ci-failed', ciAttempt: 2, ciMax: 3, summary: 'unit, build', detail: 'CI failed (2/3): unit, build.' })])
  assert.equal(view.number, 542)
  assert.equal(view.repo, 'acme/app')
  assert.equal(view.head, HEAD)
  assert.equal(view.legacy, false)
  assert.deepEqual(view.chips, [{ label: 'CI failed 2/3', tone: 'coral' }])
  assert.equal(view.notices[0]?.body, 'CI failed (2/3): unit, build.')
})

test('a plain update keeps the review chip of the same head', () => {
  const approved = post({ type: 'approved', verdict: 'approve' }, 'msg_0000000000000001', 1)
  const update = post({ type: 'update' }, 'msg_0000000000000002', 2)
  assert.deepEqual(prView([approved, update]).chips, [{ label: 'Approved by the reviewer', tone: 'sage' }])
  // A new head drops the old review.
  const nextHead = post({ type: 'update', head: 'b'.repeat(40) }, 'msg_0000000000000003', 3)
  assert.deepEqual(prView([approved, nextHead]).chips, [{ label: 'Update', tone: 'quiet' }])
})

test('every PR type has a chip', () => {
  const types: PREvent['type'][] = ['opened', 'ready', 'approved', 'changes', 'ci-failed', 'ci-paused', 'ci-passed', 'returned', 'merged', 'closed', 'comment', 'held-comment', 'review-assigned', 'update']
  for (const type of types) {
    const chip = chipOf({ type, label: 'PR #1', location: CHANGE, changeId: CHANGE })
    assert.ok(chip.label.length > 0, `${type} has no label`)
    assert.ok(chip.tone.length > 0, `${type} has no tone`)
  }
  assert.deepEqual(chipOf({ type: 'changes', label: 'PR #1', location: CHANGE, changeId: CHANGE, verdict: 'question' }), { label: 'Question for the author', tone: 'gold' })
})

test('a stored post without a payload still folds and reads its text', () => {
  const legacy: PRPostInput = {
    id: 'msg_0000000000000004', ts: 1, kind: 'pr', notice: { kind: 'approved' },
    text: `https://github.com/acme/app/pull/542\nHead: ${HEAD}\n\nacme/app PR at ${HEAD} approved by its Dyna reviewer:\n\nNo open findings.`,
  }
  const view = prView([legacy])
  assert.equal(view.legacy, true)
  assert.equal(view.number, 542)
  assert.deepEqual(view.chips, [{ label: 'Approved by the reviewer', tone: 'sage' }])
  assert.match(view.notices[0]?.body ?? '', /No open findings/)
})
