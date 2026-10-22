import assert from 'node:assert/strict'
import test from 'node:test'
import { errorText } from './errorText.ts'

test('an error gives its message, a thrown string or nullish value gives itself', () => {
  assert.equal(errorText(new Error('boom')), 'boom')
  assert.equal(errorText({ message: 'bad' }), 'bad')
  assert.equal(errorText('HTTP 500'), 'HTTP 500')
  assert.equal(errorText(null), 'null')
  assert.equal(errorText(undefined), 'undefined')
})
