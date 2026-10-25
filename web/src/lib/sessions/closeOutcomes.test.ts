import test from 'node:test'
import assert from 'node:assert/strict'
import type { CloseOutcome } from '../api/room.ts'
import { closeRows, closeSummaryText } from './closeOutcomes.ts'

// The app words each row from the typed facts alone, so an engine wording
// change never reaches this view.
test('close rows name the work, what happened and where it went', () => {
  const outcomes: CloseOutcome[] = [
    { kind: 'pr', result: 'moved', number: 42, to: 'dyna:root' },
    { kind: 'goal', result: 'moved', id: 'g7', to: 'dyna:lead' },
    { kind: 'workflow', result: 'canceled', id: 'wf_one' },
    { kind: 'question', result: 'withdrawn', id: 'q2' },
    { kind: 'child', result: 'closed', id: 'ses_x', name: 'worker-2' },
  ]
  const rows = closeRows(outcomes)
  assert.deepEqual(rows.map((r) => [r.subject, r.result]), [
    ['PR #42', 'handed over'],
    ['Goal g7', 'handed over'],
    ['Workflow wf_one', 'canceled'],
    ['Question q2', 'withdrawn'],
    ['worker-2', 'closed'],
  ])
  assert.deepEqual(rows[0]?.target, { kind: 'pr', number: 42 })
  assert.deepEqual(rows[1]?.target, { kind: 'goal', id: 'g7' })
  assert.deepEqual(rows[4]?.target, { kind: 'session', name: 'worker-2' })
})

// A failed step is a failed step, never a quiet "moved": the row says so and
// offers the one choice that resolves it.
test('a failed close step offers the choice that resolves it', () => {
  const rows = closeRows([
    { kind: 'goal', result: 'failed', id: 'g9', failure: 'goal-needs-agent' },
    { kind: 'child', result: 'failed', id: 'ses_y', name: 'worker-3', failure: 'child-handoff' },
    { result: 'failed', failure: 'process-cleanup' },
  ])
  assert.equal(rows[0]?.failed, true)
  assert.equal(rows[0]?.failure, 'No open agent can hold the goal.')
  assert.deepEqual(rows[0]?.choice, { action: 'move-goal', label: 'Hand off the goal' })
  assert.deepEqual(rows[1]?.choice, { action: 'move-child', label: 'Move the child' })
  // A failure with nothing to open offers no control, and it names the close
  // itself when it concerns no one kind of work.
  assert.equal(rows[2]?.choice, undefined)
  assert.equal(rows[2]?.subject, 'Session close')
  assert.equal(rows[2]?.failure, 'The session process did not stop.')
})

// A close summary is one line and says when a step failed.
test('a close summary is one line and says when a step failed', () => {
  const moved: CloseOutcome[] = [{ kind: 'pr', result: 'moved', number: 7, to: 'dyna:root' }]
  assert.equal(closeSummaryText(moved), 'PR #7 handed over')
  const failed: CloseOutcome[] = [{ kind: 'pr', result: 'failed', number: 7, failure: 'handoff' }]
  assert.equal(closeSummaryText(failed), 'PR #7 did not finish')
})
