import test from 'node:test'
import assert from 'node:assert/strict'
import { statusBox, isReady, classify, laneOf, mergeBox } from './prlist.ts'
import { prTracks } from './pr.ts'
import type { ChangeView } from '../api/change.ts'

// Each test sets only the fields it reads, so every ChangeView field is optional here.
type Change = Partial<ChangeView>

const approved: Change ={ state: 'open', status: 'approved', ci: 'passed', verdict: 'approve', reviewState: 'approved', head: 'current', reviewHead: 'current' }

test('lean rows keep the review action without carrying summary text', () => {
  for (const ci of ['passed', 'pending']) {
    assert.equal(statusBox({ ...approved, ci, hasReviewSummary: true }).review, true)
    assert.equal(statusBox({ ...approved, ci, hasReviewSummary: false }).review, false)
  }
})

test('a blocked review cannot become ready from a stored approval', () => {
  const stale: Change = { ...approved, reviewState: 'blocked', reviewBlocked: 'The PR head is unverified.' }
  assert.equal(isReady(stale), false)
  assert.equal(statusBox(stale).say, stale.reviewBlocked)
})

test('a draft is held in its CI or review lane and cannot land', () => {
  for (const ci of ['pending', 'passed']) {
    const draft: Change = { ...approved, isDraft: true, ci }
    assert.equal(prTracks(draft).ready, false)
    assert.equal(isReady(draft), false)
    assert.equal(classify(draft).label, 'Held')
    assert.equal(laneOf(draft), ci === 'pending' ? 'ci' : 'review')
    assert.equal(statusBox(draft).say, 'Draft: the author must mark it ready before it can land.')
  }
})

test('a lean row keeps its finding count without loading findings', () => {
  const row: Change = { ...approved, reviewRound: 2, findingCount: 3 }
  const [, review] = mergeBox(row).rows
  assert.ok(review !== undefined)
  assert.equal(review.note, '3 findings')
})
