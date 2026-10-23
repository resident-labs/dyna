import test from 'node:test'
import assert from 'node:assert/strict'
import { openPRs, mergePosition, reviewLabel, trialProgress, waitingReason, waitingWords } from './roomPRs.ts'
import type { MergeControlStatus, MergeQueueItem } from '../api/change.ts'

// Do not give a blocked PR a queue place or show a merged PR as open.
test('the open list follows merge order and keeps blocked PRs outside the next batch', () => {
  const queue: MergeQueueItem[] = [
    { changeId: 'pr2', roomId: 'r', url: '', head: 'b', state: 'trial-running', trial: { branch: 'trial/1', phase: 'running', batch: ['pr2'] } },
    { changeId: 'pr1', roomId: 'r', url: '', head: 'a', state: 'ready' },
    { changeId: 'pr3', roomId: 'r', url: '', head: 'c', state: 'conflict' },
  ]
  const rows = [1, 2, 3, 4].map((number) => ({ changeId: `pr${number}`, number, kind: 'github', state: number === 4 ? 'merged' : 'open' }))
  assert.deepEqual(openPRs(rows, queue).map((r) => r.number), [2, 1, 3])
  assert.equal(mergePosition('pr2', queue), 'Trial · 1/1')
  assert.equal(mergePosition('pr1', queue), 'Next · 1')
  assert.equal(mergePosition('pr3', queue), '')
})

test('approval must name the current head and satisfy required reviewers', () => {
  const row = { reviewState: 'approved', head: 'new', reviewHead: 'old' }
  assert.equal(reviewLabel(row).label, 'Review outdated')
  assert.equal(reviewLabel({ ...row, reviewHead: undefined }).label, 'Approval unverified')
  assert.equal(reviewLabel({ ...row, reviewHead: 'new', missingRequiredReviewers: ['s'] }).label, 'Needs required reviewers')
  assert.equal(reviewLabel({ ...row, reviewHead: 'new' }).label, 'Approved')
})

test('trial progress uses only its exact head and pause wins over main CI', () => {
  const status: MergeControlStatus = { roomId: 'r', paused: true, reason: 'The failed batch needs a decision.', queue: [], trials: [], localCIChecks: {
    old: [{ name: 'wrong head', node: 'old-node', state: 'success', status: 'completed', conclusion: 'success', url: '' }],
    current: [{ name: 'web', node: 'hermeslab', state: 'success', status: 'completed', conclusion: 'success', url: '' }, { name: 'go', node: 'hermeslab', state: 'pending', status: 'in_progress', conclusion: '', url: '' }],
  } }
  const progress = trialProgress({ branch: 'trial/1', head: 'current', phase: 'running', batch: [] }, status)
  assert.equal(progress.nodes, 'hermeslab')
  assert.equal(progress.total, 2)
  assert.equal(progress.completed, 1)
  assert.equal(waitingReason(status), null, 'a paused room has its own block, not a waiting line')
})

// The waiting line is typed facts: the kind of wait, the branch, the commit
// and the PRs it holds. It never carries the service's sentence or a full
// commit to the screen.
test('the waiting line carries the wait kind, the PR numbers and the commit', () => {
  const head = 'e1d554e3f2a52ea46a2d575803f412b2cbac2514'
  const main = { state: 'failed', head, wait: { kind: 'failed' as const, base: 'main' } }
  const status: MergeControlStatus = { roomId: 'r', paused: false, queue: [
    { roomId: 'r', changeId: 'github.com/o/r#281', url: '', head: 'h1', state: 'trial-running', main },
    { roomId: 'r', changeId: 'github.com/o/r#280', url: '', head: 'h2', state: 'trial-running', main },
    { roomId: 'r', changeId: 'github.com/o/r#279', url: '', head: 'h3', state: 'review', main: { state: 'passed', head, wait: { kind: 'passed' as const, base: 'main' } } },
  ], trials: [] }
  const reason = waitingReason(status)
  assert.ok(reason, 'a failed main CI holds the queue')
  assert.deepEqual(reason, { kind: 'failed', base: 'main', sha: head, prs: [281, 280] })
  assert.equal(waitingWords(reason), 'Default branch CI failed')
  for (const value of Object.values(reason)) {
    assert.ok(typeof value !== 'string' || !value.includes('GitHub CI'), 'no service sentence is stored')
  }
})
