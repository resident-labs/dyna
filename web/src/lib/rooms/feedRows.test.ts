import test from 'node:test'
import assert from 'node:assert/strict'
import { feedRows, feedPRRepos, isShellResult, rowPost, rowPosts } from './feedRows.ts'
import type { FeedInput, FeedRow } from './feedRows.ts'
import { need } from '../../../test/need.ts'

const start = new Date(2026, 9, 17, 12).getTime()
function result(n: number, kind = 'completed', extra: Partial<FeedInput> = {}): FeedInput {
  const id = `sh_20261017-120000-${n.toString(16).padStart(8, '0')}`
  return { id: `msg_${n}`, ts: start + n * 60_000, from: `shell:${id}`, kind,
    completion: true, text: `${kind} with exit code ${kind === 'failed' ? 1 : 0}.`,
    to: ['test-room'], ...extra }
}
const content = (rows: readonly FeedRow[]) => rows.filter((row) => row.kind === 'post')
const conts = (rows: readonly FeedRow[]) => content(rows).map((row) => row.cont)

test('automatic shell exits of every kind are shell results', () => {
  for (const kind of ['completed', 'failed', 'stopped']) assert.equal(isShellResult(result(1, kind)), true)
})

test('other posts are never shell results', () => {
  for (const post of [
    { id: 'human', from: 'test-room', ts: start, kind: 'post', human: true },
    { id: 'agent', from: 'test-room:agent', ts: start, kind: 'post' },
    result(10, 'completed', { human: true }),
    result(10, 'completed', { owner: true }),
    result(10, 'completed', { completion: false }),
    result(10, 'completed', { replyTo: 'parent-message' }),
    result(10, 'completed', { attachments: [{ id: 'file' }] }),
    result(10, 'completed', { from: 'test-room:agent' }),
    result(10, 'pr', { from: 'github:review' }),
  ]) assert.equal(isShellResult(post), false)
})

test('day and unread dividers and sender continuation', () => {
  const a = result(1), b = result(2)
  assert.deepEqual(feedRows([a, b], b.id).map((row) => row.kind), ['day', 'post', 'new', 'post'])
  const midnight = new Date(2026, 9, 18).getTime()
  assert.deepEqual(feedRows([{ ...a, ts: midnight - 1 }, { ...b, ts: midnight }]).map((row) => row.kind), ['day', 'post', 'day', 'post'])
  assert.deepEqual(feedRows([]), [])
  const posts = [1, 2].map((n) => ({ id: `message-${n}`, from: 'test-room:agent', ts: start + n * 1000, to: ['test-room'] }))
  assert.equal(conts(feedRows(posts))[1], true)
  // A post at the unread marker starts a block of its own.
  assert.equal(conts(feedRows(posts, need(posts[1]).id))[1], null)
})

test('a root post after a human post starts its own block, close behind or later', () => {
  const owner = (seconds: number, extra: Partial<FeedInput>) => ({ id: `msg_o${seconds}`, ts: start + seconds * 1000, from: 'test-room', owner: true, to: ['test-room'], text: 'x', ...extra })
  const human = (s: number) => owner(s, { human: true }), root = (s: number) => owner(s, {})
  // Same minute: before the fix, sender plus time window put root inside the human's block.
  assert.deepEqual(conts(feedRows([human(0), root(20), root(30), human(40)])), [null, false, true, false])
  // Later than the 7-minute window: a new block either way.
  assert.deepEqual(conts(feedRows([human(0), root(600)])), [null, false])
})

const prPost = (id: string, number = 542, repo = 'acme/app', extra: Partial<FeedInput> = {}): FeedInput => ({
  id, kind: 'pr', from: 'pr:watch', ts: start + Number(id.replace(/\D/g, '') || 0) * 1000,
  text: `https://github.com/${repo}/pull/${number}\nHead: 3bc9bc0b55e28c82dd4eacd8ba063315c223ea0e\n\nPR merged.`, ...extra,
})

test('consecutive notices for one PR update a stable row and retain each post', () => {
  const a = prPost('p1'), b = prPost('p2')
  const rows = content(feedRows([a, b]))
  assert.equal(rows.length, 1)
  assert.equal(need(rows[0]).key, a.id)
  assert.equal(rowPost(need(rows[0])), b)
  assert.deepEqual(rowPosts(need(rows[0])), [a, b])
})

test('PR folding stops at other messages, repos, days, unread markers and post context', () => {
  const a = prPost('p1'), b = prPost('p2')
  for (const posts of [
    [a, prPost('p2', 543)], [a, prPost('p2', 542, 'other/app')],
    [a, { id: 'chat', from: 'test-room', ts: start, text: 'Hello' }, b],
    [a, { ...b, ts: start + 86400000 }],
    [a, { ...b, replyTo: 'chat' }], [a, { ...b, attachments: [{ id: 'file' }] }],
    [a, { ...b, forward: { id: 'forward' } }],
    [{ ...a, text: 'unknown PR' }, { ...b, text: 'unknown PR' }],
  ]) assert.equal(content(feedRows(posts)).length, posts.length)
  assert.equal(content(feedRows([a, b], b.id)).length, 2)
})

test('feed PR rows name the repositories of the page', () => {
  const p = prPost('p1')
  assert.deepEqual([...feedPRRepos([p, prPost('p2')])], ['acme/app'])
  assert.equal(feedPRRepos([p, prPost('p2', 1, 'other/app')]).size, 2)
})

test('held comment notices stay separate from grouped PR updates', () => {
  const common = { kind: 'pr', from: 'dyna', to: [], text: 'https://github.com/acme/app/pull/7\n\nPR update.' }
  const posts: FeedInput[] = [
    { ...common, id: 'one', ts: 1000 },
    { ...common, id: 'held', ts: 2000, notice: { kind: 'held-comment' } },
    { ...common, id: 'three', ts: 3000 },
  ]
  assert.deepEqual(content(feedRows(posts)).map((row) => row.post.id), ['one', 'held', 'three'])
})

// A hook run posts its start and then its result. One card shows the run, so
// the start folds into the result of the same run ID.
const hookPost = (id: string, type: 'started' | 'result', runId: string, state: 'running' | 'passed' | 'failed'): FeedInput => ({
  id, ts: start, from: 'dyna', kind: 'hook', text: 'hook',
  notice: { kind: type },
  payload: { runId, repo: '/repo', hook: 'self-update', event: 'main-updated', state },
})

test('a hook run folds its start into its result, and another run is another card', () => {
  const posts = [
    hookPost('h1', 'started', 'run-a', 'running'),
    hookPost('h2', 'result', 'run-a', 'passed'),
    hookPost('h3', 'started', 'run-b', 'running'),
    hookPost('h4', 'result', 'run-b', 'failed'),
  ]
  const rows = content(feedRows(posts))
  assert.deepEqual(rows.map((row) => row.key), ['h1', 'h3'])
  assert.deepEqual(rowPosts(rows[0]).map((post) => post.id), ['h1', 'h2'])
  assert.equal(rowPost(rows[0])?.notice?.kind, 'result')
  assert.deepEqual(rowPosts(rows[1]).map((post) => post.id), ['h3', 'h4'])
})

// A merge trial posts its progress and its end. One card shows the trial, so
// every step of one trial ID folds into the newest post.
const mergePost = (id: string, type: 'queued' | 'trial' | 'checks' | 'landed' | 'failed', trialId: string): FeedInput => ({
  id, ts: start, from: 'dyna-merge', kind: 'merge', text: 'merge',
  notice: { kind: type },
  payload: { type, trialId, repo: 'acme/app', base: 'main', prs: [{ number: 242, title: 'Tidy the composer', url: 'https://github.com/acme/app/pull/242' }] },
})

test('a merge trial folds queued, trial and landed into one card', () => {
  const posts = [
    mergePost('m1', 'queued', 'dyna/merge-trial/7'),
    mergePost('m2', 'trial', 'dyna/merge-trial/7'),
    mergePost('m3', 'checks', 'dyna/merge-trial/7'),
    mergePost('m4', 'landed', 'dyna/merge-trial/7'),
    mergePost('m5', 'queued', 'dyna/merge-trial/8'),
  ]
  const rows = content(feedRows(posts))
  assert.deepEqual(rows.map((row) => row.key), ['m1', 'm5'])
  assert.deepEqual(rowPosts(rows[0]).map((post) => post.id), ['m1', 'm2', 'm3', 'm4'])
  assert.equal(rowPost(rows[0])?.notice?.kind, 'landed')
})
