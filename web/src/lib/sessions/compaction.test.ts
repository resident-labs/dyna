import test from 'node:test'
import assert from 'node:assert/strict'
import { compactTokens, compactionText } from './compaction.ts'

test('compactTokens words a context size the same way the plain view does', () => {
  assert.equal(compactTokens(0), '')
  assert.equal(compactTokens(999), '999')
  assert.equal(compactTokens(182000), '182k')
  assert.equal(compactTokens(122500), '123k')
  assert.equal(compactTokens(999499), '999k')
  assert.equal(compactTokens(1000000), '1.0M')
  assert.equal(compactTokens(1050000), '1.1M')
})

test('compactionText words each state the transcript reports', () => {
  assert.equal(compactionText({ state: 'started' }), 'Compacting context…')
  assert.equal(compactionText({ state: 'completed', trigger: 'auto', tokensBefore: 182000, tokensAfter: 41000 }), 'Context compacted · 182k → 41k tokens · auto')
  assert.equal(compactionText({ state: 'completed', trigger: 'dyna' }), 'Context compacted · dyna', 'no counts when the harness reports none')
  assert.equal(compactionText({ state: 'completed', tokensAfter: 41000 }), 'Context compacted · 41k tokens after')
  assert.equal(compactionText({ state: 'failed', reason: 'session limit' }), 'Context compaction failed: session limit')
  assert.equal(compactionText({ state: 'failed' }), 'Context compaction failed')
})
