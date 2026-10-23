import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { compile } from 'svelte/compiler'
import { viewIdentity } from './viewIdentity.ts'

test('data refreshes preserve the view identity', () => {
  const state = { roomId: 'room-1', view: 'channel:general', drill: { id: 'session-1' }, posts: [], session: null }
  assert.equal(viewIdentity(state), viewIdentity({ ...state, drill: { id: 'session-1', name: 'New name' }, posts: [{ id: 1 }], session: { turns: [1, 2] } }))
  assert.equal(viewIdentity({ roomId: 'room-1', view: 'goals' }), viewIdentity({ roomId: 'room-1', view: 'goals', drill: null, goals: [1] }))
})

test('room, view and open session changes each change the identity', () => {
  const state = { roomId: 'room-1', view: 'channel:general', drill: null }
  for (const change of [{ roomId: 'room-2' }, { view: 'goals' }, { drill: { id: 'session-1' } }]) {
    assert.notEqual(viewIdentity(state), viewIdentity({ ...state, ...change }))
  }
  assert.notEqual(viewIdentity({ ...state, drill: { id: 'session-1' } }), viewIdentity({ ...state, drill: { id: 'session-2' } }))
})

test('the main view uses only an incoming fade and skips PR views', () => {
  const source = readFileSync(new URL('../../App.svelte', import.meta.url), 'utf8')
  assert.match(source, /\{#key viewIdentity\(\{ roomId, view, drill \}\)\}/)
  assert.match(source, /class="center"[^>]*in:fade=\{\{ duration: .*motion\(FAST\)/)
  assert.match(source, /view === 'github' \|\| view === 'issues'/)
  assert.match(source, /let prView = \$derived\([^\n]*sessionTab === 'prs'\)\)/)
  assert.match(source, /in:fade=\{\{ duration: prView \? 0 : motion\(FAST\) \}\}/)
  assert.doesNotMatch(source, /(?:out|transition):fade/)
  assert.doesNotThrow(() => compile(source, { filename: 'App.svelte', generate: 'client' }))
})
