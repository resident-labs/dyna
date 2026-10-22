import test from 'node:test'
import assert from 'node:assert/strict'
import { statusBox, isReady, mergeBox, classify, moveKey } from './prlist.ts'
import { prTracks, statusMeta } from './pr.ts'
import type { ChangeView } from '../api/change.ts'

type Change = Partial<ChangeView>

const approved: Change = { state: 'open', status: 'approved', ci: 'passed', verdict: 'approve', reviewState: 'approved', head: 'h', reviewHead: 'h' }

test('a missing required reviewer blocks every ready indicator', () => {
  const waiting: Change = { ...approved, requiredReviewers: ['ses_a', 'ses_b'], missingRequiredReviewers: ['ses_b'] }
  assert.equal(isReady(approved), true)
  assert.equal(prTracks(waiting).ready, false)
  assert.equal(isReady(waiting), false)
  assert.doesNotMatch(statusMeta(waiting).label, /ready to merge/)
  assert.match(statusBox(waiting).say, /required reviewer/)
  assert.equal(classify(waiting).label, 'Needs required reviewers')
  // Once the backend clears the list, the PR reads ready again.
  assert.equal(isReady({ ...waiting, missingRequiredReviewers: [] }), true)
})

test('the merge box names the missing reviewers and keeps old rows when none are set', () => {
  const waiting: Change = { ...approved, requiredReviewers: ['ses_a', 'ses_b'], missingRequiredReviewers: ['ses_a', 'ses_b'] }
  const row = mergeBox(waiting, { names: { ses_a: 'security' } }).rows.find((r) => r.id === 'required')
  assert.equal(row?.title, 'Waiting for required reviewers')
  assert.equal(row?.note, 'Missing: security, ses_b')
  const done = mergeBox({ ...waiting, missingRequiredReviewers: [] }).rows.find((r) => r.id === 'required')
  assert.equal(done?.icon, 'ok')
  assert.deepEqual(mergeBox(approved).rows.map((r) => r.id), ['checks', 'review', 'merge'])
})

test('adding or clearing a requirement moves a displayed ready PR', () => {
  const ready: Change = { ...approved, changeId: 'c' }
  const required: Change = { ...ready, requiredReviewers: ['ses_a'], missingRequiredReviewers: ['ses_a'] }
  const cleared: Change = { ...required, missingRequiredReviewers: [] }
  assert.notEqual(moveKey([ready]), moveKey([required]))
  assert.notEqual(moveKey([required]), moveKey([cleared]))
  assert.equal(moveKey([required]), moveKey([{ ...required }]))
})
