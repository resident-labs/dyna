import test from 'node:test'
import assert from 'node:assert/strict'
import { mergeFeedPage, rerouteAnchors, withChannels } from './feedPages.js'

const feed = (posts, extra = {}) => ({ room: { id: 'room' }, posts, ...extra })

test('an older page preserves post objects, append order, and the live offset', () => {
  const current = { id: 'current', unread: true }
  const newest = { id: 'newest' }
  const old = { id: 'old' }
  const merged = mergeFeedPage(feed([current, newest], { offset: 30 }), feed([old, { id: 'current', unread: false }], { offset: 20, earlier: 4, cursor: 'next' }), true)
  assert.deepEqual(merged.posts, [old, current, newest])
  assert.equal(merged.posts[1], current)
  assert.equal(merged.offset, 30)
  assert.equal(merged.earlier, 4)
  assert.equal(merged.cursor, 'next')
})

test('a refreshed overlapping tail keeps loaded history and later live posts', () => {
  const old = { id: 'old' }, overlap = { id: 'overlap' }, live = { id: 'live' }
  const merged = mergeFeedPage(feed([old, overlap, live], { historyLoaded: true, earlier: 2, cursor: 'history' }), feed([{ id: 'overlap' }], { earlier: 30, cursor: 'head' }))
  assert.deepEqual(merged.posts, [old, overlap, live])
  assert.equal(merged.posts[1], overlap)
  assert.equal(merged.cursor, 'history')
  assert.equal(merged.earlier, 2)
})

test('a disjoint tail marks a missing newer range without moving the old window', () => {
  const page = feed([{ id: 'new' }], { earlier: 80, cursor: 'head' })
  const merged=mergeFeedPage(feed([{ id: 'old' }], { historyLoaded: true }), page)
  assert.deepEqual(merged.posts.map(p=>p.id), ['old'])
  assert.equal(merged.newer,80)
})

test('discarded newer posts return through a contiguous forward page', () => {
 const old = feed([{id:'p1'},{id:'p2'}],{earlier:1,newer:3,historyLoaded:true,cursor:'p1'})
 const next = feed([{id:'p3'},{id:'p4'}],{earlier:3,newer:1})
 const merged=mergeFeedPage(old,next,'newer')
 assert.deepEqual(merged.posts.map(p=>p.id),['p1','p2','p3','p4'])
 assert.equal(merged.newer,1)
 assert.equal(merged.earlier,1)
})

test('refreshing an old window cannot append a disjoint live tail', () => {
 const old=feed([{id:'p1'}],{earlier:1,newer:10,historyLoaded:true,cursor:'p1'})
 const merged=mergeFeedPage(old,feed([{id:'p11'}],{earlier:11,newer:0}))
 assert.deepEqual(merged.posts.map(p=>p.id),['p1'])
 assert.equal(merged.newer,10)
})

test('a page takes the server channels of loaded posts, so a promote moves them', () => {
  const kept = { id: 'kept', channels: ['general'] }
  const moved = { id: 'moved', text: 'loaded', channels: ['channel:lead'] }
  const merged = mergeFeedPage(feed([moved, kept]), feed([{ id: 'moved', text: 'page', channels: ['general'] }, { id: 'kept', channels: ['general'] }], { earlier: 2, cursor: 'c' }), true)
  assert.deepEqual(merged.posts.map((p) => p.channels), [['general'], ['general']])
  assert.equal(merged.posts[0].text, 'loaded', 'the loaded post object stays apart from its channels')
  assert.equal(merged.posts[1], kept, 'unchanged posts keep their object')
})

test('reroute pages cover a full loaded window and update only changed channels', () => {
  const posts = Array.from({ length: 300 }, (_, i) => ({ id: `p${i}`, channels: ['channel:lead'] }))
  const anchors = rerouteAnchors(posts)
  assert.ok(anchors.length <= 2)
  // The server returns up to 200 posts around each anchor.
  const index = new Map(posts.map((p, i) => [p.id, i]))
  const pages = anchors.map((id) => ({ posts: posts.slice(Math.max(0, index.get(id) - 100), index.get(id) + 100).map((p) => ({ id: p.id, channels: p.id === 'p0' ? ['channel:lead'] : ['general'] })) }))
  const out = withChannels(posts, pages)
  assert.equal(out[0], posts[0], 'an unchanged post keeps its object')
  assert.ok(out.slice(1).every((p) => p.channels[0] === 'general'), 'every other loaded post moved')
})
