import test from 'node:test'
import assert from 'node:assert/strict'
import { alsoAsked, questionID, answeredLine } from './questions.js'

test('group notes show distinct other askers with the same open question', () => {
  const q = { asker: 'one', state: 'open', groupKey: 'same' }
  const other = { ...q, asker: 'two' }
  assert.deepEqual(alsoAsked([q, other, other, { ...other, state: 'answered' }, { ...other, groupKey: 'else' }], q), [other])
})
test('a question link only reads question posts', () => {
  assert.equal(questionID({ kind: 'question', text: '[QUESTION q7] Why?' }), 'q7')
  assert.equal(questionID({ kind: 'message', text: '[QUESTION q7] Why?' }), 'q7')
})
test('open questions show the oldest first, outcomes show the newest outcome first', async () => {
  const { orderQuestions } = await import('./questions.js')
  const a = { id: 'q1', state: 'open', askedAt: '2026-10-17T12:00:00Z' }
  const b = { id: 'q2', state: 'open', askedAt: '2026-10-17T13:00:00Z' }
  assert.deepEqual(orderQuestions([b, a], 'open').map((q) => q.id), ['q1', 'q2'])
  assert.deepEqual(orderQuestions([{ ...a, state: 'answered', answer: { at: b.askedAt } }, { ...b, state: 'answered', answer: { at: a.askedAt } }], 'answered').map((q) => q.id), ['q1', 'q2'])
})

test('questions asked in the same millisecond retain their allocation order', async () => {
  const { orderQuestions } = await import('./questions.js')
  const q = (id, nano) => ({ id, state: 'open', askedAt: `2026-10-17T13:54:33.454${nano}-05:00` })
  assert.deepEqual(orderQuestions([q('q3', '863248'), q('q1', '240105'), q('q2', '593121')], 'open').map((q) => q.id), ['q1', 'q2', 'q3'])
})

test('group notes keep different recipients separate', () => {
  const question = { asker: 'one', to: 'owner', state: 'open', groupKey: 'same' }
  assert.equal(alsoAsked([question, { ...question, asker: 'two', to: 'lead' }], question).length, 0)
})

test('a post the human typed is never a question, even with the marker', () => {
  assert.equal(questionID({ kind: 'message', human: true, text: '[QUESTION q7] Why?' }), '')
})
test('an answer names who gave it: the human or root', () => {
  const answer = { choice: 'yes', text: '' }
  assert.equal(answeredLine({ answererName: 'you', answer }), 'You answered: yes')
  assert.equal(answeredLine({ answererName: 'root', answer: { choice: '', text: 'later' } }), 'Root answered: later')
})
