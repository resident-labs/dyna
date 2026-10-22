import test from 'node:test'
import assert from 'node:assert/strict'
import { prTracks, statusMeta } from './pr.ts'
import { classify, isReady, statusBox, type Row } from './prlist.ts'

test('GitHub draft status holds an approved PR across the board and detail', () => {
  const held: Row = { kind: 'pr', state: 'open', status: 'approved', isDraft: true, ci: 'passed', verdict: 'approve', reviewState: 'approved' }
  assert.equal(prTracks(held).ready, false)
  assert.equal(isReady(held), false)
  assert.deepEqual(statusMeta(held), { label: 'Held', tone: 'faint' })
  assert.deepEqual(classify(held), { group: 'review', label: 'Held', tone: 'faint' })
  assert.equal(statusBox(held).say, 'Draft: the author must mark it ready before it can land.')
  assert.equal(isReady({ ...held, isDraft: false }), true)
  assert.equal(classify({ ...held, state: 'merged' }).label, 'Merged')
})
