import test from 'node:test'
import assert from 'node:assert/strict'
import { trackedFor, itemState, sortThread, labelStyle, loadFilters, commitDays } from './github.ts'

test('trackedFor matches repo and number, and prefers a watching record', () => {
  const list = [
    { number: 7, repo: 'Acme/Widgets', status: 'untracked' },
    { number: 7, repo: 'acme/widgets', status: 'ci-running' },
    { number: 7, repo: 'other/repo', status: 'approved' },
  ]
  assert.equal(trackedFor(list, 'acme/widgets', 7)?.status, 'ci-running')
  assert.equal(trackedFor(list.slice(0, 1), 'acme/widgets', 7)?.status, 'untracked')
  assert.equal(trackedFor(list, 'acme/widgets', 8), null)
  assert.equal(trackedFor(list.slice(2), 'acme/widgets', 7), null)
  assert.equal(trackedFor(null, 'x', 1), null)
})

test('itemState names PR and issue states', () => {
  assert.equal(itemState('pr', { state: 'OPEN', isDraft: true }).label, 'Draft')
  assert.equal(itemState('pr', { state: 'MERGED' }).id, 'merged')
  assert.equal(itemState('pr', { state: 'CLOSED' }).tone, 'coral')
  assert.equal(itemState('issue', { state: 'CLOSED', stateReason: 'NOT_PLANNED' }).label, 'Not planned')
  assert.equal(itemState('issue', { state: 'CLOSED', stateReason: 'COMPLETED' }).id, 'done')
  assert.equal(itemState('issue', { state: 'OPEN' }).tone, 'sage')
})

test('sortThread orders comments and reviews and drops empty review shells', () => {
  const t = sortThread({
    comments: [{ author: { login: 'a' }, body: 'late', createdAt: '2026-10-02T00:00:00Z' }],
    reviews: [
      { author: { login: 'r' }, body: '', state: 'COMMENTED', submittedAt: '2026-10-01T00:00:00Z' },
      { author: { login: 'r' }, body: '', state: 'APPROVED', submittedAt: '2026-10-01T12:00:00Z' },
    ],
  })
  assert.deepEqual(t.map((x) => [x.author.login, x.review ?? '']), [['r', 'approved'], ['a', '']])
})

test('labelStyle refuses a color that is not hex', () => {
  assert.match(labelStyle('00ff00'), /#00ff00/)
  assert.match(labelStyle('red;background:url(x)'), /#8b8b8b/)
})

test('a thread post keeps its link, so the Checks tab can open a bot comment', () => {
  const posts = sortThread({ comments: [{ author: { login: 'coderabbitai', isBot: true }, body: 'Review limit reached', createdAt: '2026-10-02T11:00:00Z', url: 'https://x/c1' }], reviews: [{ author: { login: 'rev' }, body: 'ok', state: 'APPROVED', submittedAt: '2026-10-02T12:00:00Z' }] })
  assert.deepEqual(posts.map((p) => p.url), ['https://x/c1', undefined])
})

test('saved filters keep only the state, so old saved kinds do not matter', () => {
  Object.assign(globalThis, { localStorage: { getItem: () => JSON.stringify({ kind: 'issue', state: 'closed' }), setItem() {} } })
  assert.deepEqual(loadFilters(), { state: 'closed' })
  Reflect.deleteProperty(globalThis, 'localStorage')
})

test('commitDays groups commits by local day, newest day and commit first', () => {
  const at = (d: number, h: number) => new Date(2026, 9, d, h).toISOString()
  const days = commitDays([
    { sha: 'a', headline: 'one', date: at(1, 9) },
    { sha: 'b', headline: 'two', date: at(2, 8) },
    { sha: 'c', headline: 'three', date: at(2, 14) },
  ])
  assert.deepEqual(days.map((d) => d.commits.map((c) => c.sha)), [['c', 'b'], ['a']])
  assert.match(days[0]?.label ?? '', /Oct 2, 2026/)
  assert.deepEqual(commitDays(null), [])
})

test('commitDays merges a day split by out-of-order dates and sorts newest first', () => {
  const at = (d: number, h: number) => new Date(2026, 9, d, h).toISOString()
  const days = commitDays([
    { sha: 'a', date: at(1, 9) },
    { sha: 'b', date: at(2, 9) },
    { sha: 'c', date: at(1, 15) },
  ])
  assert.deepEqual(days.map((d) => d.commits.map((c) => c.sha)), [['b'], ['c', 'a']])
  assert.equal(new Set(days.map((d) => d.key)).size, 2)
})
