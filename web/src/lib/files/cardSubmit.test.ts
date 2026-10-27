import test from 'node:test'
import assert from 'node:assert/strict'
import { routeSubmit } from './cardSubmit.ts'

const question = (state: string) => ({ id: 'q7', post: 'p1', state, options: ['yes', 'no'] })
const card = { id: 'p1', conversation: 'dm:s9', reply: { kind: 'session' as const, address: 'dyna:builder' } }

test('a card on a question post answers the question', () => {
  const open = [question('open')]
  assert.deepEqual(routeSubmit(card, open, { text: 'ok', choice: 'yes' }), { kind: 'answer', question: 'q7', choice: 'yes', text: '' })
  assert.deepEqual(routeSubmit(card, open, { text: ' a, b ', choice: 'maybe' }), { kind: 'answer', question: 'q7', choice: '', text: 'a, b' })
  assert.deepEqual(routeSubmit(card, [question('answered')], { text: 'again' }), { kind: 'refuse', error: 'answered' })
})

test('a card on any other post replies to its author in the post conversation', () => {
  assert.deepEqual(routeSubmit(card, [question('open')].map((q) => ({ ...q, post: 'other' })), { text: 'go' }), {
    kind: 'reply', draft: '@dyna:builder go', replyTo: 'p1', conversation: 'dm:s9',
  })
  assert.deepEqual(routeSubmit({ id: 'p2' }, [], { text: 'go' }), { kind: 'refuse', error: 'no-recipient' })
})
