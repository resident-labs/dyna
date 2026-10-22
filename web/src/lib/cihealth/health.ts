// Readers and small helpers for the CI health view. The engine sends runs,
// their job bands and the slow tests; these functions turn the unknown JSON
// into typed rows and compute only what the view draws.
import { arrayOf, isBoolean, isNumber, isRecord, isString } from '../api/guards.ts'

export interface CIHealthJobBand {
  id: number
  name: string
  busyMs: number
}

export interface CIHealthRun {
  token: string
  pr: number
  head: string
  workflow: string
  started: string
  status: string
  conclusion: string
  queueMs: number
  busyMs: number
  warnMs: number
  failMs: number
  jobs: CIHealthJobBand[]
}

export interface CIHealthTestRun {
  test: string
  package: string
  token: string
  pr: number
  head: string
  at: string
  durationMs: number
  over: boolean
}

export interface CIHealthTest {
  test: string
  name: string
  package: string
  features: string[]
  limitMs: number
  lastMs: number
  typicalMs: number
  breaches: number
  state: string
  ignored: string
  runs: CIHealthTestRun[]
}

export interface CIHealth {
  room: string
  limitMs: number
  warnMs: number
  failMs: number
  runs: CIHealthRun[]
  tests: CIHealthTest[]
}

export interface CIHealthStep {
  name: string
  ms: number
}

export interface CIHealthJob {
  id: number
  name: string
  status: string
  conclusion: string
  started: string
  completed: string
  busyMs: number
  steps: CIHealthStep[]
}

export interface CIHealthRunDetail {
  run: CIHealthRun
  jobs: CIHealthJob[]
  tests: CIHealthTestRun[]
}

const rows = (v: unknown): Record<string, unknown>[] => (Array.isArray(v) ? v.filter(isRecord) : [])
const str = (v: unknown): string => (isString(v) ? v : '')
const num = (v: unknown): number => (isNumber(v) && Number.isFinite(v) ? v : 0)
const list = (v: unknown): string[] => (arrayOf(isString)(v) ? v : [])

const readBand = (v: Record<string, unknown>): CIHealthJobBand => ({ id: num(v.id), name: str(v.name), busyMs: num(v.busyMs) })

const readTestRun = (v: Record<string, unknown>): CIHealthTestRun => ({
  test: str(v.test),
  package: str(v.package),
  token: str(v.token),
  pr: num(v.pr),
  head: str(v.head),
  at: str(v.at),
  durationMs: num(v.durationMs),
  over: isBoolean(v.over) ? v.over : false,
})

const readRun = (v: Record<string, unknown>): CIHealthRun => ({
  token: str(v.token),
  pr: num(v.pr),
  head: str(v.head),
  workflow: str(v.workflow),
  started: str(v.started),
  status: str(v.status),
  conclusion: str(v.conclusion),
  queueMs: num(v.queueMs),
  busyMs: num(v.busyMs),
  warnMs: num(v.warnMs),
  failMs: num(v.failMs),
  jobs: rows(v.jobs).map(readBand),
})

const readTest = (v: Record<string, unknown>): CIHealthTest => ({
  test: str(v.test),
  name: str(v.name),
  package: str(v.package),
  features: list(v.features),
  limitMs: num(v.limitMs),
  lastMs: num(v.lastMs),
  typicalMs: num(v.typicalMs),
  breaches: num(v.breaches),
  state: str(v.state),
  ignored: str(v.ignored),
  runs: rows(v.runs).map(readTestRun),
})

const readStep = (v: Record<string, unknown>): CIHealthStep => ({ name: str(v.name), ms: num(v.ms) })

const readJob = (v: Record<string, unknown>): CIHealthJob => ({
  id: num(v.id),
  name: str(v.name),
  status: str(v.status),
  conclusion: str(v.conclusion),
  started: str(v.started),
  completed: str(v.completed),
  busyMs: num(v.busyMs),
  steps: rows(v.steps).map(readStep),
})

// readCIHealth returns the room's graph and tests, or null for a shape this
// client cannot read.
export function readCIHealth(v: unknown): CIHealth | null {
  if (!isRecord(v)) return null
  return {
    room: str(v.room),
    limitMs: num(v.limitMs),
    warnMs: num(v.warnMs),
    failMs: num(v.failMs),
    runs: rows(v.runs).map(readRun),
    tests: rows(v.tests).map(readTest),
  }
}

export function readRunDetail(v: unknown): CIHealthRunDetail | null {
  if (!isRecord(v) || !isRecord(v.run)) return null
  return {
    run: readRun(v.run),
    jobs: rows(v.jobs).map(readJob),
    tests: rows(v.tests).map(readTestRun),
  }
}

export interface CIHealthTestDetail {
  test: CIHealthTest
}

export function readTestDetail(v: unknown): CIHealthTestDetail | null {
  if (!isRecord(v) || !isRecord(v.test)) return null
  return { test: readTest(v.test) }
}

// duration writes a time the way the app writes check times: seconds under a
// minute, then minutes and seconds. A zero time is an em dash.
export function duration(value: number): string {
  if (!Number.isFinite(value) || value <= 0) return '—'
  if (value < 1000) return `${Math.round(value)} ms`
  const seconds = value / 1000
  if (seconds < 10) return `${seconds.toFixed(1)} s`
  if (seconds < 60) return `${Math.round(seconds)} s`
  const minutes = Math.floor(seconds / 60)
  const rest = Math.round(seconds - minutes * 60)
  return rest ? `${minutes}m ${rest}s` : `${minutes}m`
}

// stateLine is the one reason a row shows. It replaces the state chip.
export function stateLine(test: CIHealthTest): string {
  switch (test.state) {
    case 'ignored':
      return test.ignored ? `allowed: ${test.ignored}` : 'allowed in this room'
    case 'recurring':
      return `broke ${test.breaches} runs, an hour apart or more`
    case 'new':
      return test.breaches > 1 ? `broke ${test.breaches} runs in one episode` : 'broke one run'
    case 'quiet':
      return 'no breach in 7 days'
    default:
      return ''
  }
}

// stateSort puts what needs work first: over the limit, then by breaches, then
// by the slowest time. The engine sorts too, and this keeps a filter view sane.
export function sortTests(tests: CIHealthTest[]): CIHealthTest[] {
  return [...tests].sort((a, b) => {
    if ((a.breaches > 0) !== (b.breaches > 0)) return a.breaches > 0 ? -1 : 1
    if (a.breaches !== b.breaches) return b.breaches - a.breaches
    return b.lastMs - a.lastMs
  })
}

export type TestFilter = 'all' | 'recurring' | 'new' | 'quiet' | 'ignored'

export function filterTests(tests: CIHealthTest[], filter: TestFilter): CIHealthTest[] {
  const sorted = sortTests(tests)
  return filter === 'all' ? sorted : sorted.filter((test) => test.state === filter)
}

// bandNames is the jobs the graph draws, in the order they first appear, so
// every column and the legend name the same job with the same shade.
export function bandNames(runs: CIHealthRun[]): string[] {
  const names: string[] = []
  for (const run of runs) {
    for (const job of run.jobs) if (!names.includes(job.name)) names.push(job.name)
  }
  return names
}

// jobMs is one job's time in a run, or zero when that job did not run there.
export function jobMs(run: CIHealthRun, name: string): number {
  return run.jobs.find((job) => job.name === name)?.busyMs ?? 0
}

// rowLimit is how many test rows the view draws. A room whose limit is 1 s
// has hundreds over it, and drawing them all costs a long frame and a large
// heap for rows nobody reads past the first screen.
export const rowLimit = 50

// breachLimit is how many breaches one test's drill-down lists.
export const breachLimit = 20

// capRows keeps the first rows of a sorted list and reports how many it left
// out, so the view can say so instead of silently hiding them.
export function capRows<T>(rows: T[], max: number): { shown: T[]; hidden: number } {
  if (max <= 0 || rows.length <= max) return { shown: rows, hidden: 0 }
  return { shown: rows.slice(0, max), hidden: rows.length - max }
}

// graphScale is the tallest column the graph draws: the fail budget, or the
// longest measured busy time when that is taller, so every column and both
// budget lines share one scale. Waiting for a runner is not counted: the
// budget judges the run's busy time, so a long wait must not raise a column.
export function graphScale(runs: CIHealthRun[], failMs: number): number {
  let tallest = failMs > 0 ? failMs : 1
  for (const run of runs) {
    if (run.busyMs > tallest) tallest = run.busyMs
  }
  return tallest
}

// graphRuns picks the runs a graph draws, oldest on the left. The store
// returns newest first, so a copy is reversed for display and the newest run
// stays available for the summary. A narrow screen draws the newest few.
export function graphRuns(runs: CIHealthRun[], max: number): { shown: CIHealthRun[]; newest: CIHealthRun | null } {
  const newest = runs[0] ?? null
  const picked = max > 0 ? runs.slice(0, max) : runs
  return { shown: [...picked].reverse(), newest }
}

// bandShare is one job's part of the run's measured busy time. Parallel jobs
// overlap, so their times sum to more than the busy time; the bands divide the
// column the run actually used instead of adding up past it.
export function bandShare(run: CIHealthRun, name: string): number {
  let total = 0
  for (const job of run.jobs) total += job.busyMs
  if (total <= 0) return 0
  return jobMs(run, name) / total
}

// bandPx is one band's height in the chart, so a column reads as time.
export function bandPx(ms: number, scale: number, chart: number): number {
  if (scale <= 0) return 0
  return Math.max((ms / scale) * chart, 0)
}

// barPct is a job or step bar's width against the longest of its run.
export function barPct(ms: number, longest: number): number {
  if (longest <= 0) return 0
  return Math.min(Math.max((ms / longest) * 100, 0), 100)
}

// spark draws a test's stored runs, oldest on the left, with the limit as a
// horizontal line. values are durations in ms.
export function spark(runs: CIHealthTestRun[], width: number, height: number, limitMs: number): { points: string; limitY: number; hasLine: boolean } {
  const values = runs.map((run) => run.durationMs).reverse()
  const top = Math.max(limitMs, ...values, 1)
  const pad = 3
  const y = (value: number) => pad + (1 - value / top) * (height - 2 * pad)
  const step = values.length > 1 ? (width - 2 * pad) / (values.length - 1) : 0
  const points = values.map((value, index) => `${(pad + index * step).toFixed(1)},${y(value).toFixed(1)}`).join(' ')
  return { points, limitY: y(limitMs), hasLine: values.length > 0 }
}

// runState names how a run ended, for the column's label and the drill-down.
export function runState(run: CIHealthRun): string {
  if (run.conclusion) return run.conclusion
  if (run.status) return run.status
  return 'unknown'
}

export function failed(run: CIHealthRun): boolean {
  return run.conclusion === 'failure' || run.conclusion === 'cancelled' || run.conclusion === 'timed_out'
}

// budgetWord says where a run sits against its own budget lines.
export function budgetWord(busyMs: number, warnMs: number, failMs: number): string {
  if (failMs > 0 && busyMs >= failMs) return 'over fail'
  if (warnMs > 0 && busyMs >= warnMs) return 'over warn'
  return 'within'
}

// budgetWithAllow returns the room's budget with the test allowed and its
// reason, keeping every other field the engine sent. One list, no second one.
export function budgetWithAllow(raw: unknown, test: string, reason: string): Record<string, unknown> | null {
  if (!isRecord(raw)) return null
  const allow = list(raw.ciTestAllow)
  const notes: Record<string, unknown> = isRecord(raw.ciTestAllowNotes) ? { ...raw.ciTestAllowNotes } : {}
  notes[test] = reason
  return { ...raw, ciTestAllow: allow.includes(test) ? allow : [...allow, test], ciTestAllowNotes: notes }
}
