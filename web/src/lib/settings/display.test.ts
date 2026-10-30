import test from 'node:test'
import assert from 'node:assert/strict'
import { choiceNote, usesSelect } from './display.ts'

test('a choice of more than four values is a select; four or fewer stay a segmented control', () => {
  assert.equal(usesSelect({ kind: 'enum', options: ['release', 'checkout', 'a', 'b'] }), false)
  assert.equal(usesSelect({ kind: 'enum', options: ['openai', 'speech-to-speech', 'dashscope', 'google-live', 'stepfun'] }), true)
  assert.equal(usesSelect({ kind: 'select', options: ['only'] }), true)
  assert.equal(usesSelect({ kind: 'bool' }), false)
})

test('choiceNote gives the note of the chosen option only', () => {
  const item = { kind: 'enum', options: ['openai', 'dashscope'], optionNotes: ['Uses the ChatGPT sign-in.', 'Needs DASHSCOPE_API_KEY.'], value: 'dashscope' }
  assert.equal(choiceNote(item), 'Needs DASHSCOPE_API_KEY.')
  assert.equal(choiceNote({ ...item, value: 'openai' }), 'Uses the ChatGPT sign-in.')
  assert.equal(choiceNote({ ...item, value: 'unknown' }), '')
  assert.equal(choiceNote({ value: true }), '')
})
