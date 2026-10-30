import assert from 'node:assert/strict'
import test from 'node:test'
import { hookEntries, mergeEntries, type MergeHistoryRow } from './activity.ts'

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
  assert.equal(entries[1]?.tone, 'failed')
  assert.equal(entries[1]?.steps[0]?.text, 'failed: frontend-check: exit status 1')
})

const run = (minutesAgo: number, result = 'pass') => ({ id: `run-${minutesAgo}`, result, startedAt: new Date(now - minutesAgo * 60000).toISOString(), finishedAt: new Date(now - minutesAgo * 60000 + 1000).toISOString() })

test('passing runs fold into one row a day, and a failure keeps its own row with its reason', () => {
  const entries = hookEntries('maintenance', 'Maintenance', [run(1), run(11), run(21, 'error'), run(31), run(41, 'pending')], now, { 'run-21': { exitCode: 2, detail: '' } })
  assert.deepEqual(entries.map((e) => e.text).sort(), ['Maintenance · 3 runs passed today', 'Maintenance · failed: exit 2 in 1s', 'Maintenance · running'].sort())
  assert.deepEqual(entries.find((e) => e.tone === 'failed')?.run, { hook: 'maintenance', id: 'run-21' })
  assert.equal(hookEntries('maintenance', 'Maintenance', [run(1)], now, {})[0]?.text, 'Maintenance · passed in 1s')
})
