// Readers and small helpers for the CI health view. The engine sends runs,
// their job bands and the slow tests; these functions turn the unknown JSON
// into typed rows and compute only what the view draws.
import { arrayOf, isBoolean, isNumber, isRecord, isString } from '../api/guards.ts'
import type { CIBudgetSettings } from '../api/change.ts'

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

// barPct is a bar's size, as a percent of the chart scale.
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

export type BudgetState = 'within' | 'warn' | 'over'

// budgetState says where a run sits against its own budget lines. It is the
// only thing that colours a bar.
export function budgetState(busyMs: number, warnMs: number, failMs: number): BudgetState {
  if (failMs > 0 && busyMs >= failMs) return 'over'
  if (warnMs > 0 && busyMs >= warnMs) return 'warn'
  return 'within'
}

const stateWords: Record<BudgetState, string> = { within: 'within budget', warn: 'warning', over: 'over budget' }
export const budgetWord = (state: BudgetState): string => stateWords[state]

// statusLine is the one line above the chart: how the newest run went.
export function statusLine(run: CIHealthRun | null): string {
  if (!run) return ''
  const state = budgetState(run.busyMs, run.warnMs, run.failMs)
  const parts = [`Last run: ${duration(run.busyMs)}`]
  if (failed(run)) parts.push('failed')
  if (state === 'over') parts.push(`over the ${duration(run.failMs)} limit`)
  else if (state === 'warn') parts.push(`over the ${duration(run.warnMs)} warning`)
  else if (run.failMs > 0) parts.push(`within the ${duration(run.failMs)} limit`)
  if (run.queueMs > 0) parts.push(`queued ${duration(run.queueMs)}`)
  return parts.join(', ')
}

// slowestJobs is a run's longest jobs, longest first.
export function slowestJobs(run: CIHealthRun, max = 3): CIHealthJobBand[] {
  return run.jobs.filter((job) => job.busyMs > 0).sort((a, b) => b.busyMs - a.busyMs).slice(0, max)
}

export type JobTrend = 'slower' | 'faster' | 'steady'

export interface JobRank {
  name: string
  typicalMs: number
  trend: JobTrend
  changePct: number
}

const median = (values: number[]): number => {
  const sorted = [...values].sort((a, b) => a - b)
  const mid = Math.floor(sorted.length / 2)
  return sorted.length % 2 ? (sorted[mid] ?? 0) : ((sorted[mid - 1] ?? 0) + (sorted[mid] ?? 0)) / 2
}

// jobRanking ranks the jobs by their typical (median) time across the runs,
// longest first. The trend compares the newer half of a job's runs with the
// older half; a change under 10% is steady. runs are newest first, and a run
// that has not finished is left out.
export function jobRanking(runs: CIHealthRun[], max = 5): JobRank[] {
  const series = new Map<string, number[]>()
  for (const run of runs) {
    // A running run saves partial job times; they would read as a speed-up.
    if (run.conclusion === '' && run.status !== 'completed') continue
    for (const job of run.jobs) {
      if (job.busyMs > 0) series.set(job.name, [...(series.get(job.name) ?? []), job.busyMs])
    }
  }
  const ranks: JobRank[] = []
  for (const [name, values] of series) {
    let changePct = 0
    if (values.length >= 4) {
      const half = Math.floor(values.length / 2)
      const older = median(values.slice(half))
      if (older > 0) changePct = Math.round(((median(values.slice(0, half)) - older) / older) * 100)
    }
    const trend: JobTrend = changePct >= 10 ? 'slower' : changePct <= -10 ? 'faster' : 'steady'
    ranks.push({ name, typicalMs: median(values), trend, changePct })
  }
  return ranks.sort((a, b) => b.typicalMs - a.typicalMs).slice(0, max)
}

// budgetWithAllow returns the room's budget with the test allowed and its
// reason, keeping every other field the engine sent. One list, no second one.
export function budgetWithAllow(raw: CIBudgetSettings | null, test: string, reason: string): CIBudgetSettings | null {
  if (!raw) return null
  const allow = raw.ciTestAllow ?? []
  const notes: Record<string, string> = raw.ciTestAllowNotes ? { ...raw.ciTestAllowNotes } : {}
  notes[test] = reason
  return { ...raw, ciTestAllow: allow.includes(test) ? allow : [...allow, test], ciTestAllowNotes: notes }
}
