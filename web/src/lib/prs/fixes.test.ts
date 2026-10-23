import { test } from 'node:test'
import assert from 'node:assert/strict'
import { fixesLabel, fixPRLabel } from './fixes.ts'

test('fixesLabel names room and GitHub issues as people type them', () => {
  assert.equal(fixesLabel([{ kind: 'dyna', number: 5 }, { kind: 'github', number: 207 }]), 'D-5, #207')
  assert.equal(fixesLabel(undefined), '')
  assert.equal(fixesLabel([]), '')
})

test('fixPRLabel shows the fixing PR only while one is open', () => {
  assert.equal(fixPRLabel(42), 'Fix in #42')
  assert.equal(fixPRLabel(undefined), '')
  assert.equal(fixPRLabel(0), '')
})
