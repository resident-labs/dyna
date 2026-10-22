import test from 'node:test'
import assert from 'node:assert/strict'
import { sessionStatus } from './status.ts'

// Queueing is not active work.
test('live session labels retain queue state on every surface', () => {
  for (const withTurns of [false, true]) {
    assert.equal(sessionStatus({ state: 'live', status: 'queued', turns: 3 }, withTurns), 'queued')
  }
  assert.equal(sessionStatus({ state: 'live', status: 'running', turns: 3 }), 'working')
  assert.equal(sessionStatus({ state: 'live', status: 'running', turns: 3 }, true), 'working · turn 3')
})
test('completed, failed, idle, and closed labels share one vocabulary', () => {
  for (const [state, label] of Object.entries({ done: 'done', error: 'failed', idle: 'idle', closed: 'closed' })) {
    assert.equal(sessionStatus({ state }), label)
  }
  assert.equal(sessionStatus({ state: 'done', turns: 1 }, true), 'done · 1 turn')
  assert.equal(sessionStatus({ state: 'done', turns: 2 }, true), 'done · 2 turns')
  assert.equal(sessionStatus({ state: 'error', turns: 2 }, true), 'failed turn 2')
  assert.equal(sessionStatus({ state: 'new-state' }), 'new-state')
  assert.equal(sessionStatus(), '')
})

test('dormant status has its own label', () => { assert.equal(sessionStatus({ status: 'dormant', state: 'done' }, true), 'dormant') })
