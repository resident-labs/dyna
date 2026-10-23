import assert from 'node:assert/strict'
import test from 'node:test'
import { isComputerInfo, isDraft, isFeedSearch, isMember, isPost, isPostResult, isSendResult, isSessionReference, parseAppEvent, parseVoicePageEvent } from './guards.ts'

const post = { wake: false, id: 'm1', ts: 1, roomId: 'r', from: 'a', to: null, text: 'hi', owner: false, completion: false, fromHue: 3, speaker: 'a' }

test('isSessionReference accepts one DM reference row and rejects a missing field', () => {
  const row = { postId: 'msg_1', from: 'r:lead', speaker: 'lead', fromHue: 2, conversation: 'room', ts: 1, preview: 'hi', attachments: 1 }
  assert.equal(isSessionReference(row), true)
  assert.equal(isSessionReference({ ...row, postId: undefined }), false)
  assert.equal(isSessionReference({ ...row, attachments: '1' }), false)
})

test('isPost accepts a null recipient list and rejects a missing field', () => {
  assert.equal(isPost(post), true)
  assert.equal(isPost({ ...post, id: undefined }), false)
  assert.equal(isPost(null), false)
})

test('parseAppEvent reads each kind', () => {
  assert.deepEqual(parseAppEvent({ kind: 'rooms' }), { kind: 'rooms' })
  assert.deepEqual(parseAppEvent({ kind: 'posts', roomId: 'r', posts: [post], offset: 9 }), { kind: 'posts', roomId: 'r', posts: [post], offset: 9 })
  const change = parseAppEvent({ kind: 'goals', roomId: 'r', change: { op: 'upsert', id: 'g1', item: { id: 'g1' } } })
  assert.deepEqual(change, { kind: 'goals', roomId: 'r', change: { op: 'upsert', id: 'g1', item: { id: 'g1' } } })
})

test('parseAppEvent drops unknown kinds and bad fields', () => {
  assert.equal(parseAppEvent({ kind: 'nope' }), null)
  assert.equal(parseAppEvent({ kind: 'posts', posts: [{ id: 'x' }] }), null)
  assert.equal(parseAppEvent({ kind: 'computer', session: 's' }), null)
  assert.equal(parseAppEvent('posts'), null)
})

test('parseVoicePageEvent reads each type and drops the rest', () => {
  assert.deepEqual(parseVoicePageEvent({ type: 'state', state: 'listening' }), { type: 'state', state: 'listening' })
  assert.deepEqual(parseVoicePageEvent({ type: 'tool', id: 'c1', name: 'ask', text: '{}', state: 'running' }), { type: 'tool', id: 'c1', name: 'ask', text: '{}', state: 'running' })
  assert.equal(parseVoicePageEvent({ type: 'audio' }), null)
  assert.equal(parseVoicePageEvent({ type: 'tool' }), null)
  assert.equal(parseVoicePageEvent({ type: 'other' }), null)
})

test('isPost checks nested data and the recipient list', () => {
  assert.equal(isPost({ ...post, to: ['a', 1] }), false)
  assert.equal(isPost({ ...post, attachments: [{ id: 'a' }] }), false)
  assert.equal(isPost({ ...post, forward: { id: 'f' } }), false)
  assert.equal(isPost({ ...post, to: ['a'], pinnedAt: 'x' }), false)
  const att = { id: 'a', name: 'n', mime: 'image/png', size: 1, sha256: 'h', shots: [{ width: 1, id: 's' }], checks: [{ name: 'c', status: 'pass' }] }
  assert.equal(isPost({ ...post, to: ['a'], attachments: [att] }), true)
})

test('isComputerInfo checks the stream and apps', () => {
  const info = { session: 's', name: 'n', state: 'running', apps: null, viewers: 0, stream: { running: true, codec: 'h264', encoder: 'x' }, rssKiB: 1, streamRssKiB: 2 }
  assert.equal(isComputerInfo(info), true)
  assert.equal(isComputerInfo({ ...info, stream: {} }), false)
  assert.equal(isComputerInfo({ ...info, apps: [{ appId: 1 }] }), false)
})

test('parseAppEvent keeps every list change flag', () => {
  const change = { op: 'upsert', id: 'm', item: { a: 1 }, inHead: true, controls: true, consolidations: true }
  assert.deepEqual(parseAppEvent({ kind: 'members', roomId: 'r', change }), { kind: 'members', roomId: 'r', change })
  assert.equal(parseAppEvent({ kind: 'members', change: { op: 'upsert', id: 1 } }), null)
})

test('guards reject each malformed nested shape', () => {
  assert.equal(isPost({ ...post, to: [1] }), false)
  assert.equal(isPost({ ...post, attachments: {} }), false)
  assert.equal(isPost({ ...post, forward: 'x' }), false)
  assert.equal(isPost({ ...post, notice: null }), false)
  assert.equal(isPost({ ...post, sessionIds: { a: 1 } }), false)
  assert.equal(isPost({ ...post, detail: { kind: 'run' } }), false)
  const info = { session: 's', name: 'n', state: 'running', apps: [{ appId: 'a', title: 't' }], viewers: 0, stream: { running: true, codec: 'h264', encoder: 'x' }, rssKiB: 1, streamRssKiB: 2 }
  assert.equal(isComputerInfo(info), true)
  assert.equal(isComputerInfo({ ...info, apps: [null] }), false)
  assert.equal(isComputerInfo({ ...info, latestStatus: { text: 't' } }), false)
  assert.equal(isComputerInfo({ ...info, width: '1' }), false)
})

test('wrong-typed optional event properties give null instead of a wider event', () => {
  assert.equal(parseAppEvent({ kind: 'rooms', roomId: 5 }), null)
  assert.equal(parseAppEvent({ kind: 'posts', offset: 'x' }), null)
  assert.equal(parseAppEvent({ kind: 'goals', change: 'x' }), null)
  assert.equal(parseAppEvent({ kind: 'goals', change: { op: 'upsert', id: 'g', controls: 'yes' } }), null)
  assert.equal(parseAppEvent({ kind: 'hooks' }), null)
  assert.equal(parseVoicePageEvent({ type: 'clear', id: 5 }), null)
  assert.equal(parseVoicePageEvent({ type: 'transcript', final: 1 }), null)
})

test('absent optional properties stay absent', () => {
  assert.deepEqual(parseAppEvent({ kind: 'goals' }), { kind: 'goals' })
  assert.deepEqual(parseVoicePageEvent({ type: 'transcript', text: 'hi' }), { type: 'transcript', text: 'hi' })
})

const member = { resumable: true, name: 'w', address: 'r:w', depth: 1, children: 0, status: 'live', state: 'live', turns: 2, created: '2026-10-20T10:00:00Z', lastActivity: '2026-10-20T10:01:00Z', hue: 4 }
const postResult = { message: post, deliveries: [{ to: 'r:w', delivery: 'session-queued', turn: 3 }] }

test('isMember checks required fields and nested doing and seat', () => {
  assert.equal(isMember(member), true)
  assert.equal(isMember({ ...member, doing: { text: 'x', since: 1 }, seat: { external: true, harness: 'h', since: 't' } }), true)
  assert.equal(isMember({ ...member, hue: '4' }), false)
  assert.equal(isMember({ ...member, doing: { since: 'x' } }), false)
  assert.equal(isMember({ ...member, seat: 1 }), false)
})

test('isPost reads the views of a cached feed that still names them channels', () => {
  assert.equal(isPost({ ...post, views: ['general', 1] }), false, 'a typed views field is checked')
  assert.equal(isPost({ ...post, views: ['general'] }), true)
  assert.equal(isPost({ ...post, channels: ['general'] }), true, 'a cached post keeps its views')
  assert.equal(isPost({ ...post, channels: ['general', 1] }), false)
})

test('isPostResult accepts null deliveries and checks each delivery', () => {
  assert.equal(isPostResult(postResult), true)
  assert.equal(isPostResult({ ...postResult, deliveries: null, failed: [{ to: 'a', delivery: 'failed', error: 'e' }], sticky: '@a ' }), true)
  assert.equal(isPostResult({ ...postResult, deliveries: [{ to: 'a' }] }), false)
  assert.equal(isPostResult({ ...postResult, failed: null }), false)
  assert.equal(isPostResult({ ...postResult, message: { id: 'x' } }), false)
  assert.equal(isPostResult({ ...postResult, sticky: 1 }), false)
})

test('isSendResult takes only the payload that matches its kind', () => {
  assert.equal(isSendResult({ kind: 'post', post: postResult }), true)
  assert.equal(isSendResult({ kind: 'spawn', spawn: { member, warning: 'w' } }), true)
  assert.equal(isSendResult({ kind: 'post', spawn: { member } }), false)
  assert.equal(isSendResult({ kind: 'post', post: postResult, spawn: { member } }), false)
  assert.equal(isSendResult({ kind: 'spawn', spawn: { member: {} } }), false)
  assert.equal(isSendResult({ kind: 'other', post: postResult }), false)
  assert.equal(isSendResult(null), false)
})

test('isDraft accepts null lists and checks the nested spawn and skipped rows', () => {
  const draft = { mentions: null, text: 't', default: true, wakes: null, self: false }
  assert.equal(isDraft(draft), true)
  assert.equal(isDraft({ ...draft, mentions: ['@a'], wakes: ['a'], skipped: [{ to: 'b', reason: 'r' }], spawn: { profile: 'p', room: 'r', task: 't', error: 'e' } }), true)
  assert.equal(isDraft({ ...draft, mentions: [1] }), false)
  assert.equal(isDraft({ ...draft, skipped: [{ to: 'b' }] }), false)
  assert.equal(isDraft({ ...draft, skipped: null }), false)
  assert.equal(isDraft({ ...draft, spawn: { profile: 'p' } }), false)
  assert.equal(isDraft({ ...draft, self: undefined }), false)
})

test('isFeedSearch needs a post list and the more flag', () => {
  assert.equal(isFeedSearch({ posts: [post], more: false }), true)
  assert.equal(isFeedSearch({ posts: [], more: true }), true)
  assert.equal(isFeedSearch({ posts: null, more: false }), false)
  assert.equal(isFeedSearch({ posts: [{ ...post, id: 1 }], more: false }), false)
  assert.equal(isFeedSearch({ posts: [] }), false)
})
