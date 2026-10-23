import test from 'node:test'
import assert from 'node:assert/strict'
import { listView, type Row } from './prlist.ts'
import { need } from '../../../test/need.ts'

// A queued batch keeps each row's own state on its status dot.
test('a merge trial lists every row of the batch in the queue', () => {
  const rows = [1, 2].map((n): Row & { changeId: string } => ({ changeId: `c${n}`, number: n, kind: 'github', state: 'open', status: 'approved', ci: 'passed', verdict: 'approve', reviewState: 'approved' }))
  const trial = { branch: 'dyna/merge-trial/1', phase: 'running', batch: ['c1', 'c2'] }
  const view = listView(rows, rows.map((r) => ({ changeId: r.changeId, state: 'trial-running', trial })))
  assert.equal(view.queue.length, 2)
  assert.equal(need(view.states.c1).label, 'Testing batch')
})
