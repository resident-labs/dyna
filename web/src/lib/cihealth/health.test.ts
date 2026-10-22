import assert from 'node:assert/strict'
import test from 'node:test'
import {
  barPct,
  bandNames,
  bandPx,
  bandShare,
  budgetWithAllow,
  capRows,
  budgetWord,
  duration,
  failed,
  filterTests,
  graphRuns,
  graphScale,
  jobMs,
  readCIHealth,
  readRunDetail,
  readTestDetail,
  runState,
  spark,
  stateLine,
  type CIHealthTest,
  type CIHealthTestRun,
} from './health.ts'

const run = (over: boolean, at: string, durationMs: number): CIHealthTestRun => ({ test: 'pkg/TestX', package: 'pkg', token: at, pr: 1, head: 'a', at, durationMs, over })

const slow: CIHealthTest = {
  test: 'internal/pr/watch/TestExportNewBranchMustBeAbsent',
  name: 'TestExportNewBranchMustBeAbsent',
  package: 'internal/pr/watch',
  features: ['prs'],
  limitMs: 1000,
  lastMs: 34200,
  typicalMs: 31000,
  breaches: 7,
  state: 'recurring',
  ignored: '',
  runs: [run(true, '2026-10-21T06:12:00Z', 34200), run(true, '2026-10-20T18:40:00Z', 31000), run(false, '2026-10-19T09:55:00Z', 900)],
}

test('the reader turns the engine answer into rows and refuses junk', () => {
  const health = readCIHealth({
    room: 'dyna-agent',
    limitMs: 1000,
    warnMs: 180000,
    failMs: 240000,
    runs: [{ token: '1241/1', pr: 1902, started: '2026-10-21T06:12:00Z', busyMs: 96000, queueMs: 12000, warnMs: 180000, failMs: 240000, conclusion: 'success', jobs: [{ id: 7, name: 'go-test', busyMs: 70000 }] }],
    tests: [slow],
  })
  assert.ok(health)
  assert.equal(health.limitMs, 1000)
  assert.equal(health.runs.length, 1)
  assert.equal(health.runs[0]?.jobs[0]?.name, 'go-test')
  assert.equal(health.tests[0]?.name, 'TestExportNewBranchMustBeAbsent')
  assert.equal(health.tests[0]?.runs.length, 3)
  assert.equal(readCIHealth(null), null)
  assert.equal(readCIHealth('nope'), null)
  // A missing job list is an empty list, not a broken row.
  const bare = readCIHealth({ runs: [{ token: '1/1' }] })
  assert.deepEqual(bare?.runs[0]?.jobs, [])
})

test('a run detail keeps its jobs, steps and test names', () => {
  const detail = readRunDetail({
    run: { token: '1241/1', pr: 1902, conclusion: 'failure', queueMs: 16000, busyMs: 166000 },
    jobs: [{ id: 7, name: 'go-test', busyMs: 114000, steps: [{ name: 'Run tests', ms: 60000 }] }],
    tests: [{ test: 'pkg/TestX', package: 'pkg', token: '1241/1', durationMs: 34200, over: true }],
  })
  assert.equal(detail?.jobs[0]?.steps[0]?.ms, 60000)
  assert.equal(detail?.tests[0]?.test, 'pkg/TestX')
  assert.equal(readRunDetail({}), null)
  assert.equal(readTestDetail({ test: slow })?.test.state, 'recurring')
  assert.equal(readTestDetail(null), null)
})

test('durations read like the rest of the app', () => {
  assert.equal(duration(0), '—')
  assert.equal(duration(820), '820 ms')
  assert.equal(duration(34200), '34 s')
  assert.equal(duration(9600), '9.6 s')
  assert.equal(duration(96000), '1m 36s')
  assert.equal(duration(180000), '3m')
})

test('state reads as a sentence, not a chip', () => {
  assert.equal(stateLine(slow), 'broke 7 runs, an hour apart or more')
  assert.equal(stateLine({ ...slow, state: 'new', breaches: 1 }), 'broke one run')
  assert.equal(stateLine({ ...slow, state: 'new', breaches: 2 }), 'broke 2 runs in one episode')
  assert.equal(stateLine({ ...slow, state: 'quiet' }), 'no breach in 7 days')
  assert.equal(stateLine({ ...slow, state: 'ignored', ignored: 'waits on the trial gate' }), 'allowed: waits on the trial gate')
  assert.equal(stateLine({ ...slow, state: 'ignored' }), 'allowed in this room')
})

test('the filter reads state and keeps the work first', () => {
  const quiet: CIHealthTest = { ...slow, test: 'pkg/TestQuiet', name: 'TestQuiet', state: 'quiet', breaches: 0 }
  const fresh: CIHealthTest = { ...slow, test: 'pkg/TestNew', name: 'TestNew', state: 'new', breaches: 1 }
  const all = filterTests([quiet, fresh, slow], 'all')
  assert.deepEqual(all.map((t) => t.name), ['TestExportNewBranchMustBeAbsent', 'TestNew', 'TestQuiet'])
  assert.deepEqual(filterTests([quiet, fresh, slow], 'new').map((t) => t.name), ['TestNew'])
  assert.deepEqual(filterTests([quiet, fresh, slow], 'quiet').map((t) => t.name), ['TestQuiet'])
})

test('the graph shares one scale for bands and both budget lines', () => {
  const runs = [
    { token: 'a', pr: 1, head: '', workflow: 'CI', started: '', status: '', conclusion: '', queueMs: 12000, busyMs: 96000, warnMs: 180000, failMs: 240000, jobs: [] },
    { token: 'b', pr: 2, head: '', workflow: 'CI', started: '', status: '', conclusion: '', queueMs: 0, busyMs: 260000, warnMs: 180000, failMs: 240000, jobs: [] },
  ]
  // The longest run is taller than the fail line, so the line sits inside.
  assert.equal(graphScale(runs, 240000), 260000)
  const one = runs[0]
  assert.ok(one)
  assert.equal(graphScale([one], 240000), 240000)
  assert.equal(bandPx(130000, 260000, 176), 88)
  assert.equal(bandPx(-5, 260000, 176), 0)
  assert.equal(barPct(114000, 114000), 100)
  assert.equal(barPct(57, 114), 50)
  assert.equal(barPct(10, 0), 0)
})

test('the graph names its bands once, in the order they first appear', () => {
  const runs = [
    { token: 'a', pr: 1, head: '', workflow: 'CI', started: '', status: '', conclusion: '', queueMs: 0, busyMs: 90000, warnMs: 0, failMs: 0, jobs: [{ id: 7, name: 'go-test', busyMs: 70000 }, { id: 8, name: 'frontend', busyMs: 20000 }] },
    { token: 'b', pr: 2, head: '', workflow: 'CI', started: '', status: '', conclusion: '', queueMs: 0, busyMs: 80000, warnMs: 0, failMs: 0, jobs: [{ id: 9, name: 'go-test', busyMs: 60000 }, { id: 10, name: 'cross-build', busyMs: 20000 }] },
  ]
  assert.deepEqual(bandNames(runs), ['go-test', 'frontend', 'cross-build'])
  const first = runs[0]
  assert.ok(first)
  assert.equal(jobMs(first, 'go-test'), 70000)
  assert.equal(jobMs(first, 'cross-build'), 0)
})

test('a column is the measured busy time, not the sum of its jobs', () => {
  // Two jobs of 90 s that ran in parallel: the run used 90 s of budget.
  const parallel = { token: 'p', pr: 1, head: '', workflow: 'CI', started: '', status: '', conclusion: '', queueMs: 0, busyMs: 90000, warnMs: 180000, failMs: 240000,
    jobs: [{ id: 1, name: 'go-test', busyMs: 90000 }, { id: 2, name: 'frontend', busyMs: 90000 }] }
  // A long wait does not belong to the budget either.
  const waited = { ...parallel, token: 'w', queueMs: 600000, busyMs: 60000 }
  assert.equal(graphScale([parallel], 240000), 240000, 'parallel jobs must not raise the scale past the budget')
  assert.equal(graphScale([waited], 240000), 240000, 'a wait must not raise the scale')
  assert.equal(bandShare(parallel, 'go-test'), 0.5)
  assert.equal(bandShare(parallel, 'frontend'), 0.5)
  // The two halves fill the column, so the bands never add up past it.
  assert.equal(bandShare(parallel, 'go-test') + bandShare(parallel, 'frontend'), 1)
})

test('a run at the fail limit draws to its line at both sizes', () => {
  // The plot height is the drawn height at each size; a column measured against
  // the same scale must reach the fail line exactly, and stop at half for half.
  for (const plot of [176, 120]) {
    const fail = 240000
    const atLimit = { token: 'a', pr: 1, head: '', workflow: 'CI', started: '', status: '', conclusion: '', queueMs: 0, busyMs: fail, warnMs: 180000, failMs: fail, jobs: [] }
    const scale = graphScale([atLimit], fail)
    assert.equal(bandPx(fail, scale, plot), plot, `fail limit at ${plot}px`)
    assert.equal(bandPx(fail / 2, scale, plot), plot / 2, `half at ${plot}px`)
    assert.equal(bandPx(180000, scale, plot), (180000 / fail) * plot, `warn line at ${plot}px`)
  }
})

test('the graph draws oldest on the left and keeps the newest run', () => {
  const runs = ['n4', 'n3', 'n2', 'n1'].map((token, index) => ({
    token, pr: index, head: '', workflow: 'CI', started: '', status: '', conclusion: '',
    queueMs: 0, busyMs: 1000, warnMs: 0, failMs: 0, jobs: [],
  }))
  const wide = graphRuns(runs, 0)
  assert.deepEqual(wide.shown.map((run) => run.token), ['n1', 'n2', 'n3', 'n4'])
  assert.equal(wide.newest?.token, 'n4')
  const narrow = graphRuns(runs, 2)
  assert.deepEqual(narrow.shown.map((run) => run.token), ['n3', 'n4'], 'the newest two, oldest first')
  assert.equal(narrow.newest?.token, 'n4')
})

test('the sparkline draws oldest first and leaves the limit visible', () => {
  const line = spark(slow.runs, 118, 26, slow.limitMs)
  assert.equal(line.hasLine, true)
  const points = line.points.split(' ').map((p) => p.split(',').map(Number))
  assert.equal(points.length, 3)
  // Points run oldest to newest, and a taller time sits nearer the top, so
  // the oldest value (900 ms) is lower on the chart than the newest (34.2 s).
  const first = points[0]
  const last = points[2]
  assert.ok(first && last)
  const [firstX, firstY] = first
  const [, lastY] = last
  assert.ok(lastY !== undefined && firstY !== undefined && firstX !== undefined)
  assert.ok(firstY > lastY)
  assert.ok(line.limitY > 0 && line.limitY < 26)
  assert.equal(spark([], 118, 26, 1000).hasLine, false)
})

test('a long list keeps its first rows and counts the rest', () => {
  const rows = ['a', 'b', 'c', 'd']
  assert.deepEqual(capRows(rows, 2), { shown: ['a', 'b'], hidden: 2 })
  assert.deepEqual(capRows(rows, 9), { shown: rows, hidden: 0 })
  assert.deepEqual(capRows([], 2), { shown: [], hidden: 0 })
})

test('a run says how it ended and where it sat', () => {
  const base = { token: 'a', pr: 1, head: '', workflow: 'CI', started: '', status: 'completed', conclusion: 'success', queueMs: 0, busyMs: 1000, warnMs: 180000, failMs: 240000, jobs: [] }
  assert.equal(runState(base), 'success')
  assert.equal(runState({ ...base, conclusion: '' }), 'completed')
  assert.equal(runState({ ...base, conclusion: '', status: '' }), 'unknown')
  assert.equal(failed({ ...base, conclusion: 'failure' }), true)
  assert.equal(failed(base), false)
  assert.equal(budgetWord(96000, 180000, 240000), 'within')
  assert.equal(budgetWord(180000, 180000, 240000), 'over warn')
  assert.equal(budgetWord(250000, 180000, 240000), 'over fail')
})

test('allowing a test keeps every other budget field and adds the reason', () => {
  const budget = { ciTimeBudget: 'enforce', ciWarnAfter: '3m', ciFailAfter: '4m', ciTestMax: '1s', ciTestAllow: ['pkg/TestOld'], ciTestAllowNotes: { 'pkg/TestOld': 'old reason' }, ciNoJUnitWarning: true }
  const next = budgetWithAllow(budget, 'pkg/TestX', 'waits on the trial gate')
  assert.ok(next)
  assert.equal(next.ciTestMax, '1s')
  assert.equal(next.ciWarnAfter, '3m')
  assert.deepEqual(next.ciTestAllow, ['pkg/TestOld', 'pkg/TestX'])
  assert.deepEqual(next.ciTestAllowNotes, { 'pkg/TestOld': 'old reason', 'pkg/TestX': 'waits on the trial gate' })
  // Allowing a second time does not add a second entry.
  const again = budgetWithAllow(next, 'pkg/TestX', 'new reason')
  assert.ok(again)
  assert.deepEqual(again.ciTestAllow, ['pkg/TestOld', 'pkg/TestX'])
  assert.deepEqual(again.ciTestAllowNotes, { 'pkg/TestOld': 'old reason', 'pkg/TestX': 'new reason' })
  assert.equal(budgetWithAllow(null, 'pkg/TestX', 'x'), null)
})
