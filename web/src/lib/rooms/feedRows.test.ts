import test from 'node:test'
import assert from 'node:assert/strict'
import { feedRows, feedPRRepos, isShellResult, rowPost, rowPosts, shellJobID } from './feedRows.ts'
import type { FeedInput, FeedRow } from './feedRows.ts'
import { eventSpecs } from './events.gen.ts'
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

// isShellResultForFeed is the feed's own filter: App.svelte drops every post it
// reports, so a result row never reaches the room chat.
const isShellResultForFeed = (post: FeedInput) => isShellResult(post)

test('a typed shell result is a shell result, and names its job', () => {
  const jobs = [
    { state: 'completed', exitCode: 0 },
    { state: 'failed', exitCode: 2 },
    { state: 'stopped', exitCode: -1 },
  ] as const
  for (const job of jobs) {
    const post: FeedInput = {
      id: 'msg_shell', ts: start, from: 'dyna', kind: 'shell', notice: { kind: 'result' }, completion: true, to: ['test-room'],
      payload: { shellId: 'sh_20261017-120000-00000001', label: 'go test', ...job, output: ['FAIL'] },
    }
    assert.equal(isShellResult(post), true)
    assert.equal(shellJobID(post), 'sh_20261017-120000-00000001')
    // The room chat leaves it out: the session DM and the shell view carry it.
    assert.equal(isShellResultForFeed(post), true)
    // An owner's own shell is not a background result row.
    assert.equal(isShellResult({ ...post, owner: true }), false)
  }
  // A turn end is not a shell result, so it keeps its feed row.
  const turn: FeedInput = {
    id: 'msg_turn', ts: start, from: 'dyna', kind: 'turn', notice: { kind: 'completed' }, completion: true, to: ['test-room'],
    payload: { session: 'test-room:decoder', sessionId: 'ses_1', turn: 1, state: 'completed', summary: 'Done.' },
  }
  assert.equal(isShellResult(turn), false)
  assert.equal(isShellResult(result(1, 'completed', { kind: 'shell', from: 'dyna' })), false)
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

test('PR update outcomes close threads only on failure', () => {
  const update = (id: string, outcome: 'ok' | 'info' | 'failed'): FeedInput => ({
    ...prPost(id), notice: { kind: 'update', thread: 'pr:7' },
    payload: { type: 'update', label: 'PR #7', location: 'url', changeId: '7', outcome },
  })
  const rows = content(feedRows([update('p1', 'ok'), update('p2', 'info'), update('p3', 'failed'), update('p4', 'ok')]))
  assert.deepEqual(rows.map((row) => rowPosts(row).map((post) => post.id)), [['p1', 'p2', 'p3'], ['p4']])
})

test('old PR posts stay separate without thread keys', () => {
  assert.equal(content(feedRows([prPost('p1'), prPost('p2')])).length, 2)
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

test('each issue note keeps its own card beside the folded issue', () => {
  const note = (id: string): FeedInput => ({ ...issuePost(id, 'updated', 'i1', 'open'), notice: { kind: 'noted' }, payload: { type: 'noted', issueId: 'i1', number: 3, title: 'Two retry loops', state: 'open', filer: 'dyna:librarian', detail: id } })
  const rows = content(feedRows([issuePost('i1', 'filed', undefined, 'open'), note('n1'), note('n2'), issuePost('i3', 'closed', 'i1', 'closed')]))
  assert.deepEqual(rows.map((row) => rowPosts(row).map((post) => post.id)), [['i1'], ['n1'], ['n2'], ['i3']])
})

test('a hook run folds across an unrelated post', () => {
  const message: FeedInput = { id: 'chat', ts: start + 1, from: 'owner', text: 'Hello' }
  const rows = content(feedRows([hookPost('start', 'started', 'run-a', 'running'), message, hookPost('end', 'result', 'run-a', 'passed', start + 2)]))
  assert.deepEqual(rows.map((row) => row.post.id), ['chat', 'end'])
  assert.deepEqual(rowPosts(rows[1]).map((post) => post.id), ['start', 'end'])
})

test('a failed hook result remains visible after a later success', () => {
  const rows = content(feedRows([hookPost('start', 'started', 'run-a', 'running'), hookPost('fail', 'result', 'run-a', 'failed'), hookPost('pass', 'result', 'run-a', 'passed')]))
  assert.deepEqual(rows.map((row) => row.post.id), ['fail', 'pass'])
})

test('consecutive successful runs fold, but a live or failed run breaks the fold', () => {
  const posts = [hookPost('a', 'result', 'a', 'passed'), hookPost('b', 'result', 'b', 'passed'), hookPost('c', 'started', 'c', 'running'), hookPost('d', 'result', 'd', 'failed'), hookPost('e', 'result', 'e', 'passed')]
  const rows = content(feedRows(posts))
  assert.deepEqual(rows.map((row) => row.post.id), ['b', 'c', 'd', 'e'])
  assert.deepEqual(rowPosts(rows[0]).map((post) => post.id), ['a', 'b'])
})

// A merge trial posts its progress and its end. One card shows the trial, so
// every step of one trial ID folds into the newest post.
const mergePost = (id: string, type: 'queued' | 'trial' | 'checks' | 'landed' | 'failed', trialId: string): FeedInput => ({
  id, ts: start, from: 'dyna-merge', kind: 'merge', text: 'merge',
  notice: { kind: type, thread: `merge:${trialId}` },
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
  assert.deepEqual(rows.map((row) => row.key), ['m4', 'm5'])
  assert.deepEqual(rowPosts(rows[0]).map((post) => post.id), ['m1', 'm2', 'm3', 'm4'])
  assert.equal(rowPost(rows[0])?.notice?.kind, 'landed')
})

test('old and mixed merge history fold across unrelated posts', () => {
  const old = (id: string, type: 'queued' | 'trial' | 'landed', trial: string): FeedInput => {
    const post = mergePost(id, type, trial)
    return { ...post, notice: { kind: type } }
  }
  const posts = [old('a', 'queued', 'old'), old('b', 'trial', 'old'), old('c', 'landed', 'old'),
    old('d', 'queued', 'mixed'), { id: 'note', ts: start, from: 'user' },
    mergePost('e', 'trial', 'mixed'), mergePost('f', 'landed', 'mixed')]
  const rows = content(feedRows(posts))
  assert.deepEqual(rows.map((row) => row.post.id), ['c', 'note', 'f'])
  assert.deepEqual(rows[0]?.posts?.map((post) => post.id), ['a', 'b', 'c'])
  assert.deepEqual(rows[2]?.posts?.map((post) => post.id), ['d', 'e', 'f'])
})

const prThread = (id: string, kind: string, ts: number): FeedInput => ({
  id, ts, from: 'dyna', kind: 'pr', notice: { kind, thread: 'pr:change' },
})

test('PR lifecycle folds across an unrelated post', () => {
  const rows = content(feedRows([prThread('a', 'ready', start), { id: 'chat', ts: start + 1, from: 'owner' }, prThread('b', 'merged', start + 2)]))
  assert.deepEqual(rows.map((row) => row.post.id), ['chat', 'b'])
  assert.deepEqual(rowPosts(rows[1]).map((post) => post.id), ['a', 'b'])
})

test('a CI failure remains visible after a later success', () => {
  const rows = content(feedRows([prThread('a', 'ci-failed', start), prThread('b', 'ready', start + 1)]))
  assert.deepEqual(rows.map((row) => row.post.id), ['a', 'b'])
})

test('thread posts fold across unrelated posts at the newest position', () => {
  const a: FeedInput = { id: 'a', ts: start, from: 'dyna', kind: 'merge', notice: { kind: 'queued', thread: 'trial' } }
  const b: FeedInput = { id: 'b', ts: start + 1, from: 'user' }
  const c: FeedInput = { ...a, id: 'c', ts: start + 2 }
  const rows = content(feedRows([a, b, c], a.id))
  assert.deepEqual(rows.map((row) => row.post.id), ['b', 'c'])
  assert.deepEqual(rows[1]?.posts?.map((post) => post.id), ['a', 'c'])
  assert.deepEqual(feedRows([a, b, c], a.id).map((row) => row.kind), ['day', 'new', 'post', 'post'])
})


test('a terminal failure is not folded beneath a later post', () => {
  const spec = need(eventSpecs.find((entry) => entry.family === 'merge' && entry.type === 'failed'))
  const previous = spec.threadEnd
  spec.threadEnd = true
  try {
    const post = (id: string, kind: string, ts: number): FeedInput => ({
      id, ts, from: 'dyna', kind: 'merge', notice: { kind, thread: 'trial' },
    })
    const rows = content(feedRows([post('a', 'queued', start), post('b', 'failed', start + 1), post('c', 'queued', start + 2)]))
    assert.deepEqual(rows.map((row) => row.posts?.map((p) => p.id)), [['a', 'b'], ['c']])
  } finally {
    spec.threadEnd = previous
  }
})

test('instruction notes fold across an unrelated post, including older notes', () => {
  const instruction = (id: string, thread?: string): FeedInput => ({
    id, ts: start, from: 'dyna', kind: 'session', notice: { kind: 'instructions', thread },
    payload: { type: 'instructions', session: 'room:worker' },
  })
  const rows = content(feedRows([instruction('a'), { id: 'note', ts: start, from: 'user' }, instruction('b', 'instructions:room:worker')]))
  assert.deepEqual(rows.map((row) => row.post.id), ['note', 'b'])
  assert.deepEqual(rowPosts(rows[1]).map((post) => post.id), ['a', 'b'])
})

test('a failure between instruction notes remains a separate card', () => {
  const instruction = (id: string): FeedInput => ({
    id, ts: start, from: 'dyna', kind: 'session', notice: { kind: 'instructions', thread: 'instructions:room:worker' },
    payload: { type: 'instructions', session: 'room:worker' },
  })
  const rows = content(feedRows([instruction('a'), mergePost('failure', 'failed', 'trial'), instruction('b')]))
  assert.deepEqual(rows.map((row) => row.post.id), ['failure', 'b'])
  assert.equal(rowPost(rows[0])?.notice?.kind, 'failed')
})

test('routine check-ins fold by parent without hiding attention or other posts', () => {
  const checkin = (id: string, parent: string, attention?: 'none' | 'blocked' | 'overdue'): FeedInput => ({
    id, ts: start + Number(id.slice(1)) * 1000, from: 'dyna', kind: 'checkin',
    payload: { parent, windowStart: 1, counts: { running: 1, queued: 0, idle: 0 }, children: [], ...(attention ? { attention } : {}) },
  })
  const posts = [checkin('p1', 'a', 'none'), checkin('p2', 'b', 'none'), checkin('p3', 'a', 'blocked'), checkin('p4', 'a', 'none'), checkin('p5', 'a', 'overdue'), checkin('p6', 'a')]
  const rows = content(feedRows(posts))
  assert.deepEqual(rows.map((row) => row.post.id), ['p2', 'p3', 'p4', 'p5', 'p6'])
  assert.deepEqual(rowPosts(rows[2]).map((post) => post.id), ['p1', 'p4'])
})
