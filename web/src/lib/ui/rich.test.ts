import test from 'node:test'
import assert from 'node:assert/strict'
import { frameKey } from './rich.ts'
import type { CardAction, CardGuard } from './rich.ts'

test('a card frame reloads when the same bytes change network policy', () => {
  const closed = { id: 'same-bytes', render: 'html' }
  const open = { ...closed, net: true }
  assert.notEqual(frameKey(closed), frameKey(open), 'closed to open loads a new frame')
  assert.notEqual(frameKey(open), frameKey(closed), 'open to closed loads a new frame')
  assert.equal(frameKey(closed), frameKey({ ...closed }), 'the same card keeps its frame')
})

test('a policy change starts a fresh guard, so the new document is not a navigation', async () => {
  const { guard, onLoad, forKey } = await import('./rich.ts')
  const closed = frameKey({ id: 'same-bytes' })
  const open = frameKey({ id: 'same-bytes', net: true })
  const load = (g: CardGuard, key: string): [CardGuard, CardAction] => onLoad(forKey(g, key))
  let g = forKey(guard(), closed)
  let act: CardAction
  ;[g, act] = load(g, closed)
  assert.equal(g.state, 'shown')
  // A real navigation inside the closed card uses its one restore.
  ;[g, act] = load(g, closed)
  assert.equal(act, 'restore')
  ;[g, act] = load(g, closed)
  // Reposted with --allow-net: the new frame's first load just shows it.
  ;[g, act] = load(g, open)
  assert.deepEqual([g.state, act, g.restores], ['shown', '', 0])
  // And closed again.
  ;[g, act] = load(g, closed)
  assert.deepEqual([g.state, act, g.restores], ['shown', '', 0])
  // The same document keeps its guard: a second navigation still stops it.
  ;[g, act] = load(g, closed)
  assert.equal(act, 'restore')
  ;[g, act] = load(g, closed)
  ;[g, act] = load(g, closed)
  assert.equal(act, 'stop')
})
