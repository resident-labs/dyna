import assert from 'node:assert/strict'
import test from 'node:test'
import type { HookRow, Inputs, MergeHistoryRow } from './model.ts'
import { buildRows } from './model.ts'
import { buildTabs, hookEntries, mergeEntries } from './tabs.ts'

const now = Date.parse('2026-10-21T22:55:00Z')
const merge = (over: Partial<MergeHistoryRow>): MergeHistoryRow => ({ kind: 'trial', outcome: 'passed', at: '2026-10-21T22:40:00Z', changes: 1, detail: '', branch: 'dyna/merge-trial/7', head: '', labels: ['PR #9'], ...over })

test('a trial and its publish are one entry, and a failed trial shows its reason', () => {
  const entries = mergeEntries([
    merge({ kind: 'publish', outcome: 'published', at: '2026-10-21T22:42:00Z', head: 'abcdef0123' }),
    merge({}),
    merge({ outcome: 'failed', at: '2026-10-21T22:30:00Z', branch: 'dyna/merge-trial/6', detail: 'frontend-check: exit status 1', labels: ['PR #8', 'PR #7'] }),
    merge({ kind: 'decision', outcome: 'recorded', at: '2026-10-21T22:20:00Z', branch: '', detail: 'Retry after the cache fix', labels: [] }),
  ])
  assert.deepEqual(entries.map((e) => e.text), ['Trial 7 · PR #9', 'Trial 6 · PR #8, PR #7', 'Decision: Retry after the cache fix'])
  assert.deepEqual(entries[0]?.steps.map((s) => s.text), ['passed · published main · abcdef0'])
  assert.equal(entries[1]?.tone, 'error')
  assert.equal(entries[1]?.steps[0]?.text, 'failed: frontend-check: exit status 1')
  assert.equal(entries[2]?.tone, 'on')
})

const hook: HookRow['hook'] = { name: 'librarian-batch', label: 'Librarian', event: 'schedule', everyMinutes: 10, disabled: false }
const run = (minutesAgo: number, result = 'pass') => ({ id: `run-${minutesAgo}`, result, startedAt: new Date(now - minutesAgo * 60000).toISOString(), finishedAt: new Date(now - minutesAgo * 60000 + 1000).toISOString() })

test('passing runs fold into one row a day, and a failure keeps its own row', () => {
  const entries = hookEntries({ hook, lastRun: null }, [run(1), run(11), run(21, 'error'), run(31), run(41, 'pending')], now, {})
  assert.deepEqual(entries.map((e) => e.text).sort(), ['Librarian · 3 runs passed today', 'Librarian · failed in 1 s', 'Librarian · running'].sort())
  assert.equal(hookEntries({ hook, lastRun: null }, [run(1)], now, {})[0]?.text, 'Librarian · passed in 1 s')
  assert.equal(hookEntries({ hook, lastRun: run(2) }, [], now, {})[0]?.text, 'Librarian · passed in 1 s')
})

test('each tab gets its own one-line state and results', () => {
  const inputs: Inputs = {
    reviewer: { enabled: true, profile: 'sol', lastError: '', current: null, queued: 0, lastVerdictAt: '' }, queue: [], mergePaused: false,
    history: [merge({ outcome: 'failed', detail: 'go-test failed' })], services: null, notices: null,
    hooks: [{ hook, lastRun: run(1) }, { hook: { ...hook, name: 'self-update', label: 'Update Dyna', event: 'main-updated' }, lastRun: run(5) }],
    runs: { 'librarian-batch': [run(1), run(11)] }, reasons: {}, settings: { 'library.enabled': '["/repo"]' }, budget: null, accounts: null, usage: null, voice: null,
    changes: [{ number: 3, title: 'Fix it', verdict: 'approve', reviewAt: new Date(now - 60000).toISOString() }], testHealth: { count: 2, limitMs: 2000 }, laneSync: null, repo: '/repo',
  }
  const tabs = buildTabs(inputs, buildRows(inputs), now)
  const by = (id: string) => tabs.find((t) => t.id === id)
  assert.deepEqual(tabs.map((t) => t.id), ['merges', 'reviews', 'updates', 'librarian', 'ci', 'more'])
  assert.match(by('merges')?.line ?? '', /^Last trial failed/)
  assert.equal(by('merges')?.tone, 'error')
  assert.equal(by('reviews')?.line, 'Idle · nothing to review now')
  assert.deepEqual(by('reviews')?.entries.map((e) => e.text), ['approve #3 · Fix it'])
  assert.deepEqual(by('librarian')?.entries.map((e) => e.text), ['Librarian · 2 runs passed today'])
  assert.deepEqual(by('updates')?.entries.map((e) => e.text), ['Update Dyna · passed in 1 s'])
  assert.match(by('librarian')?.line ?? '', /^On · last passed · next in \d+ m$/)
  assert.equal(by('ci')?.line, '2 tests over 2 s')
})
