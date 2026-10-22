import { test } from 'node:test'
import assert from 'node:assert/strict'
import { askState, callPosts, callRow, callRows, shareLine, sharedItems, shareWhat, toolLabel, unseen } from './conversation.js'

const post = (id, ts, more = {}) => ({ id, ts, channels: ['voice-call'], ...more })

test('only this call: voice-call posts since it started, in order, once', () => {
  const posts = [post('a', 900), post('c', 1200), post('b', 1100), post('g', 1150, { channels: ['general'] }), post('b', 1100)]
  assert.deepEqual(callPosts(posts, 1000).map((p) => p.id), ['b', 'c'])
  assert.deepEqual(callPosts(posts, 0), [])
})

test('rows: spoken lines, requests to root and relayed answers', () => {
  const room = 'voice-test'
  assert.deepEqual(callRow({ from: room, to: [`${room}:voice-call`], source: 'voice', spoken: true, human: true }, room), { who: 'you', to: 'Voice', tag: 'spoken' })
  assert.deepEqual(callRow({ from: `${room}:voice-call`, to: [room], kind: 'voice', spoken: true }, room), { who: 'voice', to: 'you', tag: 'spoken' })
  assert.deepEqual(callRow({ from: `${room}:voice-call`, to: [room], relayOf: 'm1' }, room), { who: 'root', to: 'you', tag: 'relayed', via: true })
})

// Root's post to @voice-call (it asks Voice to tell the owner something) is
// part of the call: it shows in the conversation as root's.
test("root's post to @voice-call shows in the call as root's", () => {
  const room = 'voice-test'
  const ring = { id: 'r1', ts: 1500, channels: ['general'], from: `${room}:root`, to: [`${room}:voice-call`], text: 'CI is red on PR 978.' }
  assert.deepEqual(callPosts([ring, post('a', 1200)], 1000).map((p) => p.id), ['a', 'r1'])
  assert.deepEqual(callRow(ring, room), { who: 'root', to: 'you', tag: 'for Voice' })
  // An external root posts as the owner, but not as the owner's speech.
  assert.deepEqual(callRow({ ...ring, from: room }, room), { who: 'root', to: 'you', tag: 'for Voice' })
  // The owner's own speech to Voice stays the owner's.
  assert.equal(callRow({ from: room, to: [`${room}:voice-call`], source: 'voice', spoken: true }, room).who, 'you')
})

// Work Voice starts is a normal post from voice-call to the room (root is
// the room's owner): it shows as a "Started" line, working until a result
// is spoken in the call, then answered.
test("work Voice starts is a Started line with its state", () => {
  const room = 'voice-test'
  const ask = post('q1', 1100, { from: `${room}:voice-call`, to: [room], kind: 'message', text: 'Ask for an ETA on PR 1091.' })
  assert.deepEqual(callRow(ask, room), { who: 'voice', to: 'root', ask: true })
  assert.equal(askState(ask, [ask], room), 'working')
  const voiceLine = post('v1', 1200, { from: `${room}:voice-call`, to: [room], kind: 'voice', spoken: true })
  assert.equal(askState(ask, [ask, voiceLine], room), 'working')
  const relay = post('a1', 1300, { from: `${room}:voice-call`, to: [room], kind: 'voice', relayOf: 'm9' })
  assert.equal(askState(ask, [ask, voiceLine, relay], room), 'answered')
  const ring = post('r1', 1300, { from: `${room}:root`, to: [`${room}:voice-call`] })
  assert.equal(askState(ask, [ask, ring], room), 'answered')
})

test('rows: posts and tool calls in time order; dispatch shows only when it failed', () => {
  const room = 'voice-test'
  const ask = post('q1', 1100, { from: `${room}:voice-call`, to: [room], kind: 'message', text: 'Ask for an ETA.' })
  const said = post('v1', 1300, { from: `${room}:voice-call`, to: [room], kind: 'voice', spoken: true })
  const tools = [
    { id: 'c1', name: 'check_pr', args: '{"number":1091}', state: 'done', ts: 1200 },
    { id: 'c2', name: 'dispatch', args: '{"text":"x"}', state: 'done', ts: 1050 },
    { id: 'c3', name: 'dispatch', args: '{"text":"y"}', state: 'failed', ts: 1400 },
    { id: 'c4', name: 'check_merge_queue', args: '{}', state: 'running', ts: 1500 },
  ]
  const rows = callRows([ask, said], tools, room, ['v1'])
  assert.deepEqual(rows.map((r) => r.key), ['q1', 'tool:c1', 'v1', 'tool:c3', 'tool:c4'])
  assert.equal(rows[0].state, 'working')
  assert.deepEqual(rows[1].tool, { label: 'Checked PR 1091', state: 'done' })
  assert.equal(rows[2].tag, 'not spoken')
  assert.deepEqual(rows[3].tool, { label: 'Starting the work', state: 'failed' })
  assert.deepEqual(rows[4].tool, { label: 'Checked the merge queue', state: 'running' })
  assert.equal(toolLabel('unknown_tool', 'not json'), 'unknown_tool')
  assert.equal(toolLabel('answer_question', '{"text":"yes"}'), 'Answered your question')
})

test('a shared file is its own quiet line after its post, and a post of only files is only its lines', () => {
  const room = 'voice-test'
  const ring = { from: `${room}:root`, to: [`${room}:voice-call`] }
  const card = { id: 'a1', name: 'compare.html', mime: 'text/html', render: 'html', title: 'Provider comparison' }
  const shot = { id: 'a2', name: 'ci.png', mime: 'image/png' }
  const told = post('r1', 1100, { ...ring, text: 'I compared the providers.', attachments: [card] })
  const bare = post('r2', 1200, { ...ring, text: '', attachments: [shot, { id: 'a3', name: 'notes.txt', mime: 'text/plain' }] })
  const rows = callRows([told, bare], [], room)
  assert.deepEqual(rows.map((r) => r.key), ['r1', 'share:r1:a1', 'share:r2:a2', 'share:r2:a3'])
  assert.deepEqual(rows[1].share, { key: 'share:r1:a1', ts: 1100, postId: 'r1', who: 'root', att: card })
  assert.deepEqual(rows.slice(1).map((r) => shareWhat(r.share.att)), ['a card', 'an image', 'a file'])
  assert.equal(shareLine(rows[1].share), 'root shared “Provider comparison”')
  assert.equal(shareLine(rows[2].share), 'root shared “ci.png”')
})

test('the banner and badge show only what the owner has not opened or hidden', () => {
  const room = 'voice-test'
  const items = sharedItems([post('r1', 1100, { from: `${room}:root`, to: [`${room}:voice-call`], attachments: [{ id: 'a1', name: 'x.png', mime: 'image/png' }, { id: 'a2', name: 'y.png', mime: 'image/png' }] })], room)
  assert.deepEqual(unseen(items, new Set()).map((i) => i.key), ['share:r1:a1', 'share:r1:a2'])
  assert.deepEqual(unseen(items, new Set(['share:r1:a2'])).map((i) => i.key), ['share:r1:a1'])
  assert.deepEqual(sharedItems([post('t', 1, { text: 'no files' })], room), [])
})
