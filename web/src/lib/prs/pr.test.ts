import test from 'node:test'
import assert from 'node:assert/strict'
import type { Facts } from './pr.ts'
import { parsePRPost, statusMeta, prTracks, reviewerSlots, withoutReviewers, prsBySession, checkMeta, checkSummary, checkTime } from './pr.ts'

const url = 'https://github.com/resident-labs/dyna/pull/17'

test('parsePRPost reads a CI failure with a log tail', () => {
  const t = `${url}\nHead: abcdef1234567\n\nCI failed (2/3): test, lint. Fix the failure and push to this PR. Mark every GitHub comment with <!-- dyna:ses_1 -->.\n\nLog tail:\ntest (run 5, attempt 1):\nFAIL x`
  const p = parsePRPost(t)
  assert.equal(p.number, 17)
  assert.equal(p.repo, 'resident-labs/dyna')
  assert.equal(p.head, 'abcdef1234567')
  assert.deepEqual(p.events.map((e) => [e.kind, e.label, e.detail]), [['ci-failed', 'CI failed 2/3', 'test, lint']])
  assert.equal(p.log, 'test (run 5, attempt 1):\nFAIL x')
  assert.equal(p.body, 'CI failed (2/3): test, lint. Fix the failure and push to this PR.')
})

test('parsePRPost batches comments and finds approval', () => {
  const t = `${url}\nHead: a1\n\nGitHub review from coderabbitai[bot]:\nnit\nhttps://x\n\nGitHub comment from octocat:\nplease rename\n\nPR #17 at a1 approved by its Dyna reviewer: good. Ready for lead/merger review and merge; Dyna did not merge it.`
  const p = parsePRPost(t)
  assert.deepEqual(p.events.map((e) => e.kind), ['approved', 'comment'])
  assert.equal(p.events[1]?.label, '2 new comments')
  assert.equal(p.log, '')
})

test('parsePRPost drops the header line of a single comment', () => {
  const p = parsePRPost(`${url}\nHead: b9\n\nGitHub issue-comments from octocat:\nPlease rename it.\nhttps://x`)
  assert.equal(p.events[0]?.label, 'Comment from octocat')
  assert.equal(p.body, 'Please rename it.\nhttps://x')
})

test('parsePRPost reads the comment id header and the noise count', () => {
  const t = `${url}\nHead: c3\n\nGitHub review from coderabbitai[bot] (comment review:991):\nStream the read.\n\nComment filter: dropped 3 comments (rate-limit notice, duplicate bot comment).\n\nLog tail:\nx`
  const p = parsePRPost(t)
  assert.deepEqual(p.events.map((e) => e.label), ['Review from coderabbitai[bot]', '3 dropped as noise'])
  assert.equal(p.dropped, 3)
  assert.equal(p.body, 'Stream the read.')
  assert.equal(p.log, 'x')
})

test('parsePRPost falls back for unknown text', () => {
  const p = parsePRPost('just text')
  assert.equal(p.number, 0)
  assert.deepEqual(p.events.map((e) => e.kind), ['update'])
  assert.equal(p.body, 'just text')
})

test('statusMeta tones and failure count', () => {
  assert.deepEqual(statusMeta({ status: 'ci-failed', consecutiveFailures: 2 }), { label: 'CI failed 2/3', tone: 'coral' })
  assert.equal(statusMeta({ status: 'changes-requested' }).tone, 'gold')
  assert.equal(statusMeta({ status: 'approved' }).tone, 'sage')
  assert.equal(statusMeta({ status: 'paused', ciPaused: false, pauseReason: 'review paused: no reviewer configured for this room' }).label, 'Needs a reviewer')
  assert.equal(statusMeta({ status: 'paused', ciPaused: true, pauseReason: 'CI wakes paused' }).label, 'Paused')
})

test('prsBySession puts open PRs first', () => {
  const m = prsBySession([{ authorSession: 's', number: 3, state: 'merged', status: 'merged' }, { authorSession: 's', number: 2, state: 'open', status: 'ci-running' }])
  assert.deepEqual(m.s?.map((p) => p.number), [2, 3])
})

test('checkMeta prefers state', () => {
  assert.equal(checkMeta({ state: 'failure' }).tone, 'coral')
  assert.equal(checkMeta({ status: 'in_progress' }).word, 'running')
  assert.equal(checkMeta({ status: 'completed', conclusion: 'success' }).word, 'passed')
})

test('review state reads in plain words on the chip', () => {
  const row = (reviewState: string, extra: Facts = {}): Facts => ({ status: 'awaiting-review', reviewState, ...extra })
  const label = (pr: Facts) => statusMeta(pr).label
  assert.equal(label(row('queued', { queuePosition: 2 })), 'Queued for review (2nd)')
  assert.equal(label(row('queued', { queuePosition: 1 })), 'Queued for review (1st)')
  assert.equal(label(row('queued', { queuePosition: 3 })), 'Queued for review (3rd)')
  assert.equal(label(row('queued', { queuePosition: 11 })), 'Queued for review (11th)')
  assert.equal(label(row('queued', { queuePosition: 12 })), 'Queued for review (12th)')
  assert.equal(label(row('queued')), 'Queued for review')
  assert.equal(label(row('reviewing')), 'Reviewing')
  assert.equal(statusMeta(row('reviewing')).tone, 'iris')
  assert.equal(label(row('approved', { status: 'approved', ci: 'passed' })), 'Approved: ready to merge')
  assert.equal(label(row('changes', { status: 'changes-requested' })), 'Changes requested')
  assert.equal(label(row('off', { status: 'paused' })), 'Review off')
  assert.deepEqual(statusMeta(row('blocked', { status: 'paused', reviewBlocked: 'Every profile is failing' })), { label: 'Every profile is failing', tone: 'gold' })
  assert.equal(label(row('blocked', { status: 'paused' })), 'Review blocked')
})

test('review state does not hide CI failures, CI pauses or ended PRs', () => {
  assert.equal(statusMeta({ status: 'ci-failed', reviewState: 'queued' }).label, 'CI failed')
  assert.equal(statusMeta({ status: 'paused', ciPaused: true, reviewState: 'queued' }).label, 'Paused')
  assert.equal(statusMeta({ status: 'merged', reviewState: 'approved' }).label, 'Merged')
  assert.equal(statusMeta({ status: 'paused', pauseReason: 'x' }).label, 'Paused')
})

test('reviewer slots read the pool', () => {
  const first = { session: { id: 's1' }, current: { pr: 12 } }
  const r = { enabled: true, reviewers: [first, { session: { id: 's2' }, current: { pr: 13 } }, { session: { id: 's3' } }], queue: [{ pr: 14 }] }
  assert.equal(reviewerSlots(r).length, 3)
  assert.deepEqual(reviewerSlots({ ...r, reviewers: [] }), [])
  assert.deepEqual(reviewerSlots({}), [])
  assert.deepEqual(reviewerSlots(null), [])
})

test('reviewer sessions and their helpers leave every list but the PR screen', () => {
  const members = [
    { id: 'me', name: 'lead' },
    { id: 'r1', name: 'reviewer-197', role: 'reviewer' },
    { id: 'r2', name: 'reviewer-198' },
    { id: 'h1', name: 'helper', parent: 'r1' },
    { id: 'h2', name: 'deep helper', parent: 'h1' },
    { id: 'a1', name: 'author', parent: 'me' },
  ]
  const names = (list: { name: string }[]) => list.map((m) => m.name)
  // The role marker alone hides a reviewer and everything below it.
  assert.deepEqual(names(withoutReviewers(members)), ['lead', 'reviewer-198', 'author'])
  // The pool status names a reviewer that has no marker yet.
  assert.deepEqual(names(withoutReviewers(members, { reviewers: [{ session: { id: 'r2' } }] })), ['lead', 'author'])
  assert.deepEqual(names(withoutReviewers(members, { reviewers: [{ session: { id: 'r2' } }], helpers: [{ id: 'a1' }] })), ['lead'])
  assert.deepEqual(withoutReviewers(null), [])
})

test('parsePRPost keeps a verdict summary as markdown and drops the repeated lead-in', () => {
  const t = 'https://github.com/o/r/pull/251\nHead: abc1234\n\nPR #251 at abc1234 approved by its Dyna reviewer:\n\n# Review PR #251\n\n## Summary\n- one\n- two\n\nReady to merge; Dyna did not merge it.'
  const pr = parsePRPost(t)
  assert.equal(pr.events[0]?.kind, 'approved')
  assert.equal(pr.body, '# Review PR #251\n\n## Summary\n- one\n- two\n\nReady to merge; Dyna did not merge it.')
})

test('parsePRPost does not read quoted watcher phrases in a verdict summary as notices', () => {
  const t = 'https://github.com/o/r/pull/251\nHead: abc1234\n\nPR #251 at abc1234 approved by its Dyna reviewer:\n\n## Summary\n\nThe watcher pauses CI after it failed CI three times in a row.\n\nPR merged. Close the branch.\n\nReady to merge; Dyna did not merge it.'
  assert.deepEqual(parsePRPost(t).events.map((e) => e.kind), ['approved'])
})

test('parsePRPost strips the verdict lead-in from a batched post', () => {
  const t = 'https://github.com/o/r/pull/251\nHead: abc1234\n\nGitHub comment from octocat:\nplease rename\n\nPR #251 at abc1234 approved by its Dyna reviewer:\n\n## Summary\nok'
  const pr = parsePRPost(t)
  assert.ok(!pr.body.includes('approved by its Dyna reviewer'))
  assert.ok(pr.body.includes('## Summary'))
})

test('CI and review run in parallel; ready needs both on the same head', () => {
  assert.deepEqual(prTracks({ ci: 'pending', reviewState: 'reviewing' }), { ci: 'running', approved: false, held: false, missing: [], ready: false })
  assert.equal(prTracks({ ci: 'pending', verdict: 'approve' }).ready, false)
  assert.equal(prTracks({ ci: 'passed', verdict: 'approve', reviewState: 'approved', head: 'b', reviewHead: 'a' }).ready, false)
  assert.equal(prTracks({ ci: 'passed', verdict: 'approve', reviewState: 'approved', head: 'b', reviewHead: 'b' }).ready, true)
  // The server sends status ci-running while CI is pending (roomsvc prStatus).
  const pending = (o: Facts) => statusMeta({ status: 'ci-running', ci: 'pending', ...o }).label
  assert.equal(pending({ reviewState: 'queued', queuePosition: 1 }), 'Queued for review (1st) · CI running')
  assert.equal(pending({ reviewState: 'reviewing' }), 'Reviewing · CI running')
  assert.equal(pending({ reviewState: 'approved', verdict: 'approve' }), 'Approved · CI running')
  assert.equal(pending({}), 'CI running')
  assert.equal(statusMeta({ status: 'ci-failed', ci: 'failed', reviewState: 'reviewing' }).label, 'CI failed')
})

test('parsePRPost reads a structured ready notice as one ready row', () => {
  const head = '8ceb989f4dd2aff97e96eb0c1248f2d81887439d'
  const text = `${url}\nHead: ${head}\n\nPR #577 is ready to land at ${head}.\n\nComment filter: dropped 0 comments.`
  const p = parsePRPost(text, { kind: 'ready' })
  assert.deepEqual(p.events.map((e) => [e.kind, e.label, e.tone]), [['ready', 'Ready to land', 'sage']])
  // The row says it is ready and shows the short head; the body repeats nothing.
  assert.equal(p.body, '')
  assert.equal(p.head, head)
})

test('checkSummary is one rule for how the checks stand', () => {
  const st = (...states: (string | { status: string; conclusion: string })[]) => checkSummary(states.map((s, i) => ({ name: `c${i}`, ...(typeof s === 'string' ? { state: s } : s) }))).state
  assert.equal(st(), 'none')
  assert.equal(st('success', 'failure'), 'failed')
  assert.equal(st('success', 'pending'), 'running')
  assert.equal(st('success', 'cancelled'), 'incomplete')
  assert.equal(st('success', { status: 'completed', conclusion: 'action_required' }), 'incomplete')
  // Skipped and neutral checks do not block, as on GitHub.
  assert.equal(st('success', 'skipped', { status: 'completed', conclusion: 'neutral' }), 'passed')
  const sum = checkSummary([{ name: 'a', state: 'success' }, { name: 'b', state: 'skipped' }, { name: 'c', state: 'cancelled' }])
  assert.deepEqual([sum.total, sum.passed.length, sum.skipped.length, sum.open.map((c) => c.name)], [3, 1, 1, ['c']])
})

test('checkTime formats a suite duration, and shows nothing when the check has none', () => {
  assert.equal(checkTime({}), '')
  assert.equal(checkTime({ durationMs: 0 }), '')
  assert.equal(checkTime({ durationMs: 340 }), '340 ms')
  assert.equal(checkTime({ durationMs: 14000 }), '14.0 s')
  assert.equal(checkTime({ durationMs: 4200 }), '4.2 s')
  assert.equal(checkTime({ durationMs: 107000 }), '1m 47s')
})
