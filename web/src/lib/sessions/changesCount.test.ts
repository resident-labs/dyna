import test from 'node:test'
import assert from 'node:assert/strict'
import { trackChanges, changedCount, type TrackEnv } from './changesCount.ts'

interface FakeEnv extends TrackEnv {
  advance(ms: number): void
  focus(): void
  timers: Map<number, { fn: () => void; at: number }>
  listeners: Map<string, () => void>
}

function fakeEnv(): FakeEnv {
  let now = 1000, id = 0
  const timers = new Map<number, { fn: () => void; at: number }>()
  const listeners = new Map<string, () => void>()
  return {
    document: { visibilityState: 'visible' },
    Date: { now: () => now },
    setTimeout: (fn, ms) => { timers.set(++id, { fn, at: now + ms }); return id },
    clearTimeout: (i) => { if (typeof i === 'number') timers.delete(i) },
    addEventListener: (name, fn) => { listeners.set(name, fn) },
    removeEventListener: (name) => { listeners.delete(name) },
    advance(ms) { now += ms; for (const [i, t] of [...timers]) if (t.at <= now) { timers.delete(i); t.fn() } },
    focus() { listeners.get('focus')?.() },
    timers, listeners,
  }
}

test('a clean session is discovered as dirty on activity, without focus or a timer', () => {
  const env = fakeEnv(); let count = 0, files = 0
  const t = trackChanges(() => { count = files }, 2000, env)
  assert.equal(count, 0)
  assert.equal(env.timers.size, 0, 'no polling timer while idle')
  files = 2 // the agent edits files
  env.advance(2500)
  t.poke()
  assert.equal(count, 2)
})

test('a burst of activity makes at most one load per wait, ending with a last one', () => {
  const env = fakeEnv(); let calls = 0
  const t = trackChanges(() => calls++, 2000, env)
  assert.equal(calls, 1)
  for (let i = 0; i < 20; i++) t.poke()
  assert.equal(calls, 1)
  env.advance(1999); assert.equal(calls, 1)
  env.advance(1); assert.equal(calls, 2)
  t.poke(); t.poke(); env.advance(2000)
  assert.equal(calls, 3)
})

test('focus loads, a hidden page does not, and stop clears everything', () => {
  const env = fakeEnv(); let calls = 0
  const t = trackChanges(() => calls++, 2000, env)
  env.advance(2000); env.focus()
  assert.equal(calls, 2)
  env.document.visibilityState = 'hidden'; env.advance(2000); t.poke()
  assert.equal(calls, 2)
  t.poke(); t.stop()
  assert.equal(env.timers.size, 0)
  assert.equal(env.listeners.size, 0)
})

test('the Changes tab counts only a linked worktree, never the main checkout', () => {
  const files = [{ path: 'a' }, { path: 'b' }]
  assert.equal(changedCount({ worktree: true, files }), 2)
  assert.equal(changedCount({ worktree: false, files }), 0, 'a stray file in the main checkout shows no tab')
  assert.equal(changedCount({ files }), 0)
  assert.equal(changedCount(null), 0)
  assert.equal(changedCount({ worktree: true }), 0)
})
