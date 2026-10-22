import test from 'node:test'
import assert from 'node:assert/strict'
import { queueLine, listView, type MergeItem, type Row } from './prlist.ts'
import { need } from '../../../test/need.ts'

// The queue header says where the batch stands in a few words; each row's
// own state stays on its status dot.
test('the queue header names the batch phase in a few words', () => {
  const rows = [1, 2].map((n): Row & { changeId: string } => ({ changeId: `c${n}`, number: n, kind: 'github', state: 'open', status: 'approved', ci: 'passed', verdict: 'approve', reviewState: 'approved' }))
  const trial = { branch: 'dyna/merge-trial/1', phase: 'running', batch: ['c1', 'c2'] }
  const view = listView(rows, rows.map((r) => ({ changeId: r.changeId, state: 'trial-running', trial })))
  assert.equal(view.queue.length, 2)
  assert.deepEqual(queueLine(view.queue), { text: 'batch running', tone: 'gold' })
  assert.equal(need(view.states.c1).label, 'Testing batch')
})

test('every running phase reads as batch running; publishing merges; a failure says so', () => {
  const line = (trial: NonNullable<MergeItem['trial']>) => queueLine([{ item: { state: 'trial-running', trial } }])
  for (const phase of ['reviewing', 'building', 'running', 'stacking']) assert.deepEqual(line({ phase, reason: 'running (2 of 4 jobs done)' }), { text: 'batch running', tone: 'gold' })
  for (const phase of ['publishing', 'published']) assert.deepEqual(line({ phase }), { text: 'merging', tone: 'sage' })
  assert.deepEqual(line({ phase: 'failed' }), { text: 'batch failed', tone: 'coral' })
})
