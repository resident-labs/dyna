import test from 'node:test'
import assert from 'node:assert/strict'
import { feedRows, feedPRRepos, isShellResult, roomPosts, rowPost, rowPosts } from './feedRows.ts'
import type { FeedInput, FeedRow } from './feedRows.ts'
import { hookEvent } from './eventPayloads.ts'
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
const hookPost = (id: string, type: 'started' | 'result', runId: string, state: 'running' | 'passed' | 'failed' | 'error', ts = start, hook = 'self-update'): FeedInput => ({
  id, ts, from: 'dyna', kind: 'hook', text: 'hook',
  notice: { kind: type },
  payload: { runId, repo: '/repo', hook, event: 'main-updated', state },
})

// A parent check-in belongs to the parent's DM. It is not part of the room's
// own conversation, so room chat does not list it, unless the owner received it.
const checkinPost = (id: string, to: string): FeedInput => ({
  id, ts: start, from: 'dyna', kind: 'checkin', text: 'check-in',
  notice: { kind: 'summary', producer: 'parent-checkin' },
  payload: {
    parent: 'ses_1', windowStart: 1, counts: { running: 0, queued: 0, idle: 1 },
    children: [{ name: 'lead', state: 'idle', age: '5m', goal: 'g1: work', pr: 'no open PRs' }],
  },
  to: [to],
})

test('a session-audience event lives in its session DM, not in room chat', () => {
  const owner: FeedInput = { id: 'msg_owner', ts: start, from: 'room:lead', text: 'Hello', to: ['room'] }
  const posts = [checkinPost('msg_parent', 'room:lead'), checkinPost('msg_owner', 'room'), owner]
  assert.deepEqual(roomPosts(posts, 'room').map((post) => post.id), ['msg_owner', 'msg_owner'])
})

// An issue is filed, then changed, then closed. One card shows the issue, so
// all three posts fold into the filing's row: a filing has no issue ID yet,
// and its own post ID is the issue's ID.
const issuePost = (id: string, type: 'filed' | 'updated' | 'closed', issueId: string | undefined, state: 'open' | 'goal' | 'closed'): FeedInput => ({
  id, ts: start, from: 'dyna', kind: 'issue', text: 'issue',
  notice: { kind: type },
  payload: { type, issueId, number: 3, title: 'Two retry loops', state, filer: 'dyna:librarian' },
})

test('an issue folds its filing, change and close into one card', () => {
  const posts = [
    issuePost('i1', 'filed', undefined, 'open'),
    issuePost('i2', 'updated', 'i1', 'goal'),
    issuePost('i3', 'closed', 'i1', 'closed'),
    issuePost('i4', 'filed', undefined, 'open'),
  ]
  const rows = content(feedRows(posts))
  assert.deepEqual(rows.map((row) => row.key), ['i1', 'i4'])
  assert.deepEqual(rowPosts(rows[0]).map((post) => post.id), ['i1', 'i2', 'i3'])
  assert.equal(rowPost(rows[0])?.notice?.kind, 'closed')
  assert.deepEqual(rowPosts(rows[1]).map((post) => post.id), ['i4'])
})

// A passing run of a hook, one a minute, as the engine posts them.
const pass = (n: number, hook = 'librarian') => hookPost(`pass-${hook}-${n}`, 'result', `${hook}-run-${n}`, 'passed', start + n * 60_000, hook)

test('a hook run shows its start only while the run is open, then its result', () => {
  const posts = [
    hookPost('h1', 'started', 'run-a', 'running'),
    hookPost('h2', 'result', 'run-a', 'passed'),
  ]
  const rows = content(feedRows(posts))
  assert.equal(rows.length, 1)
  assert.deepEqual(rowPosts(need(rows[0])).map((post) => post.id), ['h2'])
  assert.equal(rowPost(need(rows[0]))?.notice?.kind, 'result')
  assert.deepEqual(need(rows[0]).hook, { count: 1, lastTs: start })
})

test('a dozen passes of one hook fold into one row with the count and the last time', () => {
  const posts = Array.from({ length: 12 }, (_, i) => pass(i + 1))
  const rows = content(feedRows(posts))
  assert.equal(rows.length, 1)
  assert.deepEqual(need(rows[0]).hook, { count: 12, lastTs: start + 12 * 60_000 })
  assert.equal(rowPost(need(rows[0]))?.id, 'pass-librarian-12')
  assert.equal(rowPosts(need(rows[0])).length, 12)
})

test('a failure splits the fold and shows on its own', () => {
  const posts = [
    pass(1), pass(2),
    hookPost('fail-3', 'result', 'librarian-run-3', 'failed', start + 3 * 60_000, 'librarian'),
    pass(4),
  ]
  const rows = content(feedRows(posts))
  assert.deepEqual(rows.map((row) => row.post.id), ['pass-librarian-2', 'fail-3', 'pass-librarian-4'])
  assert.deepEqual(need(rows[0]).hook, { count: 2, lastTs: start + 2 * 60_000 })
  assert.equal(need(rows[1]).hook, undefined)
  assert.deepEqual(need(rows[2]).hook, { count: 1, lastTs: start + 4 * 60_000 })
})

test('two hooks never fold into one row', () => {
  const rows = content(feedRows([pass(1), pass(2, 'self-update'), pass(3)]))
  assert.deepEqual(rows.map((row) => hookEvent(row.post)?.hook), ['librarian', 'self-update'])
  assert.deepEqual(need(rows[0]).hook, { count: 2, lastTs: start + 3 * 60_000 })
  assert.deepEqual(need(rows[1]).hook, { count: 1, lastTs: start + 2 * 60_000 })
})

test('a pass folds across another hook card but not across a message', () => {
  const message: FeedInput = { id: 'chat', ts: start + 2 * 60_000, from: 'dyna:root', kind: 'message', text: 'Hello' }
  const rows = content(feedRows([pass(1), message, pass(3)]))
  assert.deepEqual(rows.map((row) => row.post.id), ['pass-librarian-1', 'chat', 'pass-librarian-3'])
})

test('passes more than the fold window apart start a new row', () => {
  const late = hookPost('pass-late', 'result', 'librarian-run-late', 'passed', start + 90 * 60_000, 'librarian')
  const rows = content(feedRows([pass(1), late]))
  assert.deepEqual(rows.map((row) => row.post.id), ['pass-librarian-1', 'pass-late'])
})

test('a running run shows on its hook row, and an older start keeps its place', () => {
  const running = hookPost('start-r', 'started', 'librarian-run-r', 'running', start + 2 * 60_000, 'librarian')
  const live = content(feedRows([pass(1), running]))
  assert.equal(live.length, 1)
  assert.equal(rowPost(need(live[0]))?.id, 'start-r')
  assert.deepEqual(need(live[0]).hook, { count: 1, lastTs: start + 60_000, active: { runId: 'librarian-run-r', ts: start + 2 * 60_000 } })
  // A later pass of the hook does not end that run: the row keeps saying that
  // a run is going until the run's own result posts.
  const later = content(feedRows([pass(1), running, pass(3)]))
  assert.equal(later.length, 1)
  assert.deepEqual(rowPosts(need(later[0])).map((post) => post.id), ['pass-librarian-1', 'start-r', 'pass-librarian-3'])
  assert.deepEqual(need(later[0]).hook, { count: 2, lastTs: start + 3 * 60_000, active: { runId: 'librarian-run-r', ts: start + 2 * 60_000 } })
})

test('an overlapping run stays visibly running until its own result arrives', () => {
  const posts = [
    hookPost('start-a', 'started', 'librarian-run-a', 'running', start, 'librarian'),
    hookPost('start-b', 'started', 'librarian-run-b', 'running', start + 60_000, 'librarian'),
    hookPost('pass-b', 'result', 'librarian-run-b', 'passed', start + 90_000, 'librarian'),
  ]
  const rows = content(feedRows(posts))
  assert.equal(rows.length, 1)
  assert.deepEqual(rowPosts(need(rows[0])).map((post) => post.id), ['start-a', 'pass-b'])
  // The row keeps saying that a run of this hook is going, and names the run
  // whose log the card opens.
  assert.deepEqual(need(rows[0]).hook, { count: 1, lastTs: start + 90_000, active: { runId: 'librarian-run-a', ts: start } })
  // A's own result ends the active run, and the row then shows the passes.
  const ended = content(feedRows([...posts, hookPost('pass-a', 'result', 'librarian-run-a', 'passed', start + 120_000, 'librarian')]))
  assert.deepEqual(need(ended[0]).hook, { count: 2, lastTs: start + 120_000 })
})

test('a run that ends in another row stops showing as active', () => {
  const message: FeedInput = { id: 'chat', ts: start + 100_000, from: 'dyna:root', kind: 'message', text: 'Hello' }
  const posts = [
    hookPost('start-a', 'started', 'librarian-run-a', 'running', start, 'librarian'),
    hookPost('start-b', 'started', 'librarian-run-b', 'running', start + 60_000, 'librarian'),
    hookPost('pass-b', 'result', 'librarian-run-b', 'passed', start + 90_000, 'librarian'),
    message,
    hookPost('pass-a', 'result', 'librarian-run-a', 'passed', start + 120_000, 'librarian'),
  ]
  const rows = content(feedRows(posts))
  assert.deepEqual(rows.map((row) => row.post.id), ['pass-b', 'chat', 'pass-a'])
  for (const row of rows) assert.equal(row.hook?.active, undefined, `${row.post.id} still claims a live run`)
})

test('the start of a run that ended badly shows nowhere', () => {
  const posts = [
    pass(1),
    hookPost('start-x', 'started', 'librarian-run-x', 'running', start + 2 * 60_000, 'librarian'),
    hookPost('fail-x', 'result', 'librarian-run-x', 'error', start + 3 * 60_000, 'librarian'),
    pass(4),
  ]
  const rows = content(feedRows(posts))
  assert.deepEqual(rows.map((row) => row.post.id), ['pass-librarian-1', 'fail-x', 'pass-librarian-4'])
})

test('the unread marker stays on a hidden first unread start', () => {
  const at = start + 600_000
  const first = hookPost('start-u', 'started', 'librarian-run-u', 'running', at, 'librarian')
  const fail = hookPost('fail-u', 'result', 'librarian-run-u', 'failed', at + 60_000, 'librarian')
  const message: FeedInput = { id: 'chat', ts: at + 120_000, from: 'dyna:root', kind: 'message', text: 'Hello' }
  assert.deepEqual(feedRows([first, fail, message], first.id).map((row) => row.kind), ['day', 'new', 'post', 'post'])
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
