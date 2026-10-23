import { test } from 'node:test'
import assert from 'node:assert/strict'
import { answers, rootQuestion } from './attention.ts'

test("the card shows root's oldest open question to the owner", () => {
  const q = (id: string, at: string, more: Partial<{ state: string; askerName: string; options: string[] }> = {}) => ({ id, askedAt: at, state: 'open', to: 'owner', askerName: 'root', text: id, ...more })
  const list = [q('q3', '2026-10-19T15:20:00Z'), q('q2', '2026-10-19T15:10:00Z'), q('q1', '2026-10-19T15:00:00Z', { state: 'answered' }), q('q4', '2026-10-19T14:00:00Z', { askerName: 'worker' })]
  assert.equal(rootQuestion(list)?.id, 'q2')
  assert.equal(rootQuestion([]), null)
  // AnswerQuestion(room, id, choice, text): a choice must be one of the
  // options, so Yes and No without options go as text.
  assert.deepEqual(answers(q('a', '')), [{ label: 'Yes', choice: '', text: 'Yes' }, { label: 'No', choice: '', text: 'No' }])
  assert.deepEqual(answers(q('a', '', { options: ['Rerun', 'Skip'] })), [{ label: 'Rerun', choice: 'Rerun', text: '' }, { label: 'Skip', choice: 'Skip', text: '' }])
})
