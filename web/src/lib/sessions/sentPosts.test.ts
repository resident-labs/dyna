import test from 'node:test'
import assert from 'node:assert/strict'
import { dmView, type DmItem, type DmTurn, type Receipt } from './dm.ts'
import type { Attachment } from '../api/room.ts'
import { need } from '../../../test/need.ts'
import { isKind } from '../../../test/session-fixtures.ts'

const room = { id: 'room_test', name: 'alpha' }
const member = { id: 'w', name: 'w', address: 'alpha:w', state: 'idle', turns: 1 }
const ts = (s: number) => `2026-10-18T10:00:${String(s).padStart(2, '0')}Z`
const queue: Receipt[] = [
  { id: 'queued', from: 'alpha', text: 'Check the logs', ts: ts(1), state: 'waiting', cancellable: true },
  { id: 'claimed', from: 'alpha', text: 'Keep the fix small', ts: ts(2), state: 'delivering', turn: 1 },
  { id: 'read', from: 'alpha', text: 'Show the result', ts: ts(3), state: 'delivering', turn: 1 },
]
const posts = queue.map((q) => ({ ...q, to: [member.address], owner: true, human: true }))
const lastQueued = need(queue[2])
const turn: DmTurn = { n: 1, status: 'ok', started: ts(0), items: [{ kind: 'steering', postId: lastQueued.id, delivered: true, from: 'alpha (human)', text: lastQueued.text, ts: ts(4) }] }
const batchedItems: DmItem[] = [
  { kind: 'prompt', postId: 'msg_owner', from: 'alpha (human)', text: 'Check the design', delivered: true, ts: ts(0) },
  { kind: 'prompt', postId: 'msg_peer', from: 'alpha:engine-cpu', text: 'The checks passed', delivered: true, ts: ts(0) },
]

const session = { member, turns: [turn], posts }

test('sent posts keep one stable transcript row through queued, delivered, and read', () => {
  const rows = dmView(session, room, {}, queue).rows.filter(isKind('message'))
  assert.deepEqual(rows.map((r) => [r.key, r.text, r.delivery]), queue.map((q, i) => [`post:${q.id}`, q.text, ['queued', 'delivered', 'read'][i]]))
  const first = need(queue[0])
  const waiting = need(dmView({ member, turns: [], posts: [] }, room, {}, [first]).rows[0])
  const claimed = need(dmView({ member, turns: [], posts: [] }, room, {}, [{ ...first, state: 'delivering' }]).rows[0])
  assert.equal(waiting.key, claimed.key)
  assert.equal(waiting.delivery, 'queued')
  assert.equal(claimed.delivery, 'delivered')
})

test('only your newest read message says read; pending ones keep their state', () => {
  const steer = (postId: string, s: number, extra: Partial<DmItem> = {}): DmItem => ({ kind: 'steering', postId, delivered: true, from: 'alpha (human)', text: postId, ts: ts(s), ...extra })
  const items: DmItem[] = [steer('a', 1), steer('b', 2), { kind: 'steering', from: 'alpha (human)', text: 'legacy', delivered: true, ts: ts(3) }, steer('c', 4), steer('d', 5, { delivered: false })]
  const rows = dmView({ member, turns: [{ ...turn, items }], posts: [] }, room).rows.filter(isKind('message'))
  assert.deepEqual(rows.map((r) => [r.text, r.delivery, !!r.quietReceipt]), [['a', 'read', true], ['b', 'read', true], ['legacy', 'read', true], ['c', 'read', false], ['d', 'delivered', false]])
})

test('identical posts in one turn keep independent receipts and keys', () => {
  const repeated = queue.slice(0, 2).map((q, i) => ({ ...q, text: 'Same instruction', state: i ? 'waiting' : 'delivering', turn: i ? 0 : 1 }))
  const turns: DmTurn[] = [{ ...turn, items: [{ kind: 'steering', postId: need(repeated[0]).id, delivered: true, from: 'alpha (human)', text: 'Same instruction', ts: ts(5) }] }]
  const before = dmView({ member, turns: [], posts: [] }, room, {}, repeated).rows
  const after = dmView({ member, turns, posts: [] }, room, {}, repeated).rows.filter(isKind('message'))
  assert.deepEqual(after.map((r) => r.key), before.map((r) => r.key))
  assert.deepEqual(after.map((r) => r.delivery), ['read', 'queued'])
  const both = repeated.map((q) => ({ ...q, state: 'delivering', turn: 1 }))
  const firstTurn = need(turns[0])
  firstTurn.items?.push({ ...need(firstTurn.items?.[0]), postId: need(repeated[1]).id, ts: ts(6) })
  const read = dmView({ member, turns, posts: [] }, room, {}, both).rows.filter(isKind('message'))
  assert.deepEqual(read.map((r) => r.key), before.map((r) => r.key))
  assert.deepEqual(read.map((r) => [r.delivery, !!r.quietReceipt]), [['read', true], ['read', false]], 'only the newest read message shows read')
})

test('post IDs preserve one row through framing, truncation, and whitespace changes', () => {
  const q: Receipt = { ...need(queue[1]), id: 'framed' }
  const attachment: Attachment = { id: 'file_test', name: 'report.txt', mime: 'text/plain', size: 0, sha256: '' }
  const original = { ...posts[1], id: q.id, attachments: [attachment] }
  for (const text of [q.text + '\n\n[attachments]\nreport.txt', 'Keep the fix… [Truncated: 12 of 100 bytes]', `  ${q.text}\n`]) {
    const turns: DmTurn[] = [{ ...turn, items: [{ kind: 'steering', postId: q.id, delivered: true, from: 'alpha (human)', text, ts: ts(5) }] }]
    const rows = dmView({ member, turns, posts: [original] }, room, {}, [q]).rows.filter(isKind('message'))
    assert.equal(rows.length, 1)
    const only = need(rows[0])
    assert.equal(only.key, `post:${q.id}`)
    assert.equal(only.text, q.text)
    assert.equal(only.delivery, 'read')
    assert.deepEqual(only.post?.attachments, [attachment])
  }
})

test('dispatched steering stays delivered and dismissible until acknowledgement', () => {
  const q = need(queue[1])
  const item: DmItem = { kind: 'steering', postId: q.id, delivered: false, from: 'alpha (human)', text: q.text, ts: ts(5) }
  const turns: DmTurn[] = [{ ...turn, items: [item] }]
  const rows = () => dmView({ member, turns, posts: [need(posts[1])] }, room, {}, [q]).rows.filter(isKind('message'))
  assert.equal(rows().length, 1)
  assert.equal(need(rows()[0]).delivery, 'delivered')
  assert.equal(need(rows()[0]).pending?.id, q.id)
  const key = need(rows()[0]).key
  item.delivered = true
  assert.equal(need(rows()[0]).key, key)
  assert.equal(need(rows()[0]).delivery, 'read')
  assert.equal(need(rows()[0]).pending, undefined)
})

test('a transcript loaded before its queue still links the original post once', () => {
  const last = need(queue[2])
  const rows = dmView(session, room).rows.filter((r) => r.kind === 'message' && r.text === last.text)
  assert.equal(rows.length, 1)
  assert.equal(need(rows[0]).key, `post:${last.id}`)
  assert.equal(need(rows[0]).delivery, 'read')
})

test('incoming peer receipts preserve thread history and counts, and stay in the thread chip', () => {
  const peer = 'alpha:peer'
  const incoming = posts.slice(0, 2).map((p) => ({ ...p, from: peer, owner: false, human: false }))
  const receipt: Receipt = { ...need(queue[1]), from: peer }
  const turns: DmTurn[] = [{ ...turn, items: [{ kind: 'steering', postId: receipt.id, delivered: true, from: peer, text: receipt.text, ts: ts(5) }] }]
  for (const history of [incoming, incoming.slice(1)]) {
    const dm = dmView({ member, turns, posts: history }, room, {}, [receipt])
    assert.equal(dm.threads.length, 1)
    assert.equal(need(dm.threads[0]).key, peer)
    assert.deepEqual(need(dm.threads[0]).posts.map((p) => p.id), history.map((p) => p.id))
    assert.equal(dm.rows.filter(isKind('thread')).length, 1)
    assert.equal(dm.rows.filter((r) => r.kind === 'message' && r.text === receipt.text).length, 0)
  }
})

test('owner text queue entries retain human identity without a stored transcript post', () => {
  for (const human of [true, false]) {
    for (const state of ['waiting', 'delivering']) {
      const q: Receipt = { ...need(queue[0]), human, state }
      const rows = dmView({ member, turns: [], posts: [] }, room, {}, [q]).rows
      assert.equal(rows.length, 1)
      assert.equal(need(rows[0]).human, human)
    }
  }
})


test('the reasoning setting and queue receipts remain independent', () => {
  const source = { member, posts: [], turns: [{ ...turn, items: [{ kind: 'thought', text: 'A thought', ts: ts(0) }] }] }
  for (const showReasoning of [true, false]) {
    const first = need(queue[0])
    const rows = dmView(source, room, { reasoning: showReasoning }, [first]).rows
    assert.equal(rows.some((r) => r.kind === 'thought'), showReasoning)
    assert.equal(need(rows.find((r) => r.key === `post:${first.id}`)).delivery, 'queued')
  }
})

test('batched prompt posts keep their real senders: the owner inline, a peer in its thread', () => {
  const dm = dmView({ member, posts: [], turns: [{ ...turn, items: batchedItems }] }, room)
  assert.deepEqual(dm.rows.filter(isKind('message')).map((r) => [r.key, r.author, r.human, r.text]), [
    ['post:msg_owner', 'alpha', true, 'Check the design'],
  ])
  assert.deepEqual(dm.threads.map((t) => [t.key, t.posts.map((p) => [p.id, p.text])]), [['alpha:engine-cpu', [['msg_peer', 'The checks passed']]]])
})
