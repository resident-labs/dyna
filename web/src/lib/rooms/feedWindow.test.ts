import test from 'node:test'
import assert from 'node:assert/strict'
import { trimFeed } from './feedWindow.ts'
import type { FeedPage, FeedPost } from './feedWindow.ts'

type TestPost = FeedPost & { text?: string }
type TestFeed = FeedPage<TestPost> & { posts: TestPost[]; offset?: number }

const feed = (n: number, earlier = 0): TestFeed => ({ room: { id: 'r' }, earlier, posts: Array.from({ length: n }, (_, i) => ({ id: `p${i}` })) })

test('trimFeed keeps the newest posts and counts the rest as earlier', () => {
  const trimmed = trimFeed(feed(7, 5), 3)
  assert.deepEqual(trimmed.posts.map((p) => p.id), ['p4', 'p5', 'p6'])
  assert.equal(trimmed.earlier, 9)
  assert.equal(trimmed.cursor, 'p4')
  assert.deepEqual(trimmed.room, { id: 'r' })
})

test('trimFeed leaves a feed up to twice the window as it is', () => {
  const f = feed(6, 2)
  assert.equal(trimFeed(f, 3), f)
  assert.equal(trimFeed(null, 3), null)
})

test('loading older history drops the far newer page and marks it for re-fetch', () => {
 const trimmed = trimFeed(feed(7, 5), 3, true)
 assert.deepEqual(trimmed.posts.map(p => p.id), ['p0', 'p1', 'p2'])
 assert.equal(trimmed.earlier, 5)
 assert.equal(trimmed.newer, 4)
})
