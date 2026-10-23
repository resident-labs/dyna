import assert from 'node:assert/strict'
import test from 'node:test'
import {
  barPct,
  budgetState,
  budgetWithAllow,
  capRows,
  duration,
  failed,
  filterTests,
  graphRuns,
  graphScale,
  jobRanking,
  readCIHealth,
  readTestDetail,
  runState,
  slowestJobs,
  spark,
  statusLine,
  stateLine,
  type CIHealthRun,
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

const ciRun = (token: string, over: Partial<CIHealthRun> = {}): CIHealthRun => ({
  token, pr: 1, head: '', workflow: 'CI', started: '', status: 'completed', conclusion: 'success',
  queueMs: 0, busyMs: 1000, warnMs: 120000, failMs: 240000, jobs: [], ...over,
})

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

test('the chart scale holds the fail budget and the longest run; a wait never raises it', () => {
  const runs = [ciRun('a', { busyMs: 96000 }), ciRun('b', { busyMs: 260000 })]
  assert.equal(graphScale(runs, 240000), 260000)
  assert.equal(graphScale([ciRun('c', { busyMs: 90000, queueMs: 600000 })], 240000), 240000)
  assert.equal(barPct(120000, 240000), 50)
  assert.equal(barPct(10, 0), 0)
})

test('the chart draws oldest on the left and keeps the newest run', () => {
  const runs = ['n4', 'n3', 'n2', 'n1'].map((token) => ciRun(token))
  const all = graphRuns(runs, 0)
  assert.deepEqual(all.shown.map((r) => r.token), ['n1', 'n2', 'n3', 'n4'])
  assert.equal(all.newest?.token, 'n4')
})

test('the sparkline draws oldest first and leaves the limit visible', () => {
  const line = spark(slow.runs, 118, 26, slow.limitMs)
  assert.equal(line.hasLine, true)
  const points = line.points.split(' ').map((p) => p.split(',').map(Number))
  assert.equal(points.length, 3)
  // A taller time sits nearer the top: the oldest value (900 ms) is lower than the newest (34.2 s).
  const [, firstY] = points[0] ?? []
  const [, lastY] = points[2] ?? []
  assert.ok(lastY !== undefined && firstY !== undefined)
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

test('a run says how it ended and where it sat against its budget', () => {
  const base = ciRun('a', { warnMs: 180000, failMs: 240000 })
  assert.equal(runState(base), 'success')
  assert.equal(runState({ ...base, conclusion: '' }), 'completed')
  assert.equal(runState({ ...base, conclusion: '', status: '' }), 'unknown')
  assert.equal(failed({ ...base, conclusion: 'failure' }), true)
  assert.equal(failed(base), false)
  assert.equal(budgetState(96000, 180000, 240000), 'within')
  assert.equal(budgetState(180000, 180000, 240000), 'warn')
  assert.equal(budgetState(250000, 180000, 240000), 'over')
  assert.equal(budgetState(250000, 0, 0), 'within', 'no budget set means nothing is over')
})

test('the status line leads with the newest run, its budget state and its wait', () => {
  assert.equal(statusLine(null), '')
  assert.equal(statusLine(ciRun('a', { busyMs: 190000, failMs: 120000, warnMs: 60000, queueMs: 40000 })), 'Last run: 3m 10s, over the 2m limit, queued 40 s')
  assert.equal(statusLine(ciRun('a', { busyMs: 70000, warnMs: 60000, failMs: 120000 })), 'Last run: 1m 10s, over the 1m warning')
  assert.equal(statusLine(ciRun('a', { busyMs: 30000, warnMs: 60000, failMs: 120000 })), 'Last run: 30 s, within the 2m limit')
  assert.equal(statusLine(ciRun('a', { busyMs: 30000, conclusion: 'failure', warnMs: 60000, failMs: 120000 })), 'Last run: 30 s, failed, within the 2m limit')
})

test('a run lists its slowest three jobs, longest first', () => {
  const jobs = [['a', 10], ['b', 50], ['c', 30], ['d', 40], ['e', 0]].map(([name, ms], id) => ({ id, name: String(name), busyMs: Number(ms) * 1000 }))
  assert.deepEqual(slowestJobs(ciRun('x', { jobs })).map((j) => j.name), ['b', 'd', 'c'])
})

test('the job ranking sorts by typical time and reads the trend from the newer half', () => {
  // Newest first: go-test got slower (60, 60 against 30, 30); lint is steady.
  const runs = [60, 60, 30, 30].map((ms, i) => ciRun(`r${i}`, { jobs: [{ id: 1, name: 'go-test', busyMs: ms * 1000 }, { id: 2, name: 'lint', busyMs: 5000 }] }))
  const ranks = jobRanking(runs)
  assert.deepEqual(ranks.map((r) => r.name), ['go-test', 'lint'])
  assert.equal(ranks[0]?.typicalMs, 45000)
  assert.equal(ranks[0]?.trend, 'slower')
  assert.equal(ranks[0]?.changePct, 100)
  assert.equal(ranks[1]?.trend, 'steady')
  // Fewer than four runs is too little to call a trend.
  assert.equal(jobRanking(runs.slice(0, 3))[0]?.trend, 'steady')
})

test('allowing a test keeps every other budget field and adds the reason', () => {
  const budget = { ciTimeBudget: 'enforce', ciBudgetExempt: null, ciWarnAfter: '3m', ciFailAfter: '4m', ciTestMax: '1s', ciTestAllow: ['pkg/TestOld'], ciTestAllowNotes: { 'pkg/TestOld': 'old reason' }, ciNoJUnitWarning: true }
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
