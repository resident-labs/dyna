import test from 'node:test'
import assert from 'node:assert/strict'
import { nextAction } from './nextAction.ts'
import type { Row, MergeItem } from './prlist.ts'

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
