import test from 'node:test'
import assert from 'node:assert/strict'
import { nextAction } from './nextAction.ts'
import { mergeBox, type Row, type MergeItem } from './prlist.ts'

test('CI actions carry a typed kind into the detail box', () => {
  for (const ci of ['pending', 'absent', 'failed'] as const) {
    const row: Row = { ci, state: 'open' }
    assert.equal(nextAction(row).kind, 'ci')
    assert.equal(mergeBox(row).isCI, true)
  }
  assert.equal(mergeBox({ ci: 'passed', state: 'open' }).isCI, false)
})

test('the detail merge headline matches the list action', () => {
  const row: Row = { ci: 'failed', state: 'open' }
  const merge: MergeItem = { state: 'conflict' }
  assert.equal(mergeBox(row, { merge }).rows.find((r) => r.id === 'merge')?.title, nextAction(row, merge).label)
})

test('next action names blockers in order and ready work', () => {
  const cases: [Row, MergeItem | null, string, string[]][] = [
    [{ ci: 'failed' }, null, 'Blocked: CI failed', []],
    [{}, { state: 'conflict' }, 'Blocked: conflict', []],
    [{ reviewState: 'changes' }, null, 'Changes requested', []],
    [{ reviewState: 'changes', verdict: 'question' }, null, 'Review question', []],
    [{ ci: 'absent', verdict: 'approve' }, { state: 'ci' }, 'CI missing', []],
    [{ ci: 'passed' }, { state: 'ci' }, 'Waiting for CI', []],
    [{ isDraft: true }, null, 'Draft', []],
    [{}, { state: 'held' }, 'Held', []],
    [{ ci: 'failed' }, { state: 'conflict' }, 'Blocked: CI failed', ['Blocked: conflict']],
    [{}, { state: 'ready' }, 'Ready to merge', []],
  ]
  for (const [row, merge, label, others] of cases) {
    const action = nextAction(row, merge)
    assert.equal(action.label, label)
    assert.deepEqual(action.others, others)
  }
})
