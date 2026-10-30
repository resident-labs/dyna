// The Services page's data model: one row per service with its switch and
// profile, the activity timeline, and what runs next. Every value comes from a
// call the engine already serves; a field with no source stays empty and the
// page shows it as unknown. Nothing here changes data.

import type { RegistryService, RegistryServiceId, RegistryState } from '../api/service.ts'

export type Tone = 'live' | 'on' | 'off' | 'error'

// ServiceInfo is one ListServices row.
export interface ServiceInfo {
  role: string
  name: string
  profile: string
  pinned: string
  reason: string
  profiles: string[]
}

// ReviewerInfo is ReviewerStatus as the page needs it.
export interface ReviewerInfo {
  enabled: boolean
  profile: string
  lastError: string
  current: { pr: number; title: string } | null
  queued: number
  lastVerdictAt: string
}

// QueueRow is one RoomMergeStatus item.
export interface QueueRow {
  state: string
  reason: string
  trial?: { branch: string; batch: string[] | null }
}

export interface NoticeRow {
  key: string
  severity: string
  text: string
}

// HookRow is one HookSummary row: the hook and its newest run.
export interface HookRow {
  hook: { name: string; label: string; event: string; everyMinutes: number; disabled: boolean }
  lastRun: { id: string; result: string; startedAt: string; finishedAt: string } | null
}

export interface MergeHistoryRow {
  detail: string
  kind: string
  outcome: string
  at: string
  changes: number
  // branch is the trial branch that a trial row and its publish row share, and
  // labels name the changes in it. head is the tested or published commit.
  branch: string
  head: string
  labels: string[]
}

// HookRunRow is one run of a hook, as the hook run list gives it. id names the
// run, so its log can open.
export interface HookRunRow {
  id: string
  result: string
  startedAt: string
  finishedAt: string
}

// RunReason is why one hook run failed, read from its log: the exit code a
// command returned, or the log's last line when it records no exit code, such
// as a timeout or a runner fault.
export interface RunReason {
  exitCode: number
  detail: string
}

export interface UsageRow {
  role: string
  turns: number
  usd: number | null
  incomplete: boolean
}

export interface BudgetInfo {
  warnAfter: string
  failAfter: string
  testMax: string
}

export interface AccountsInfo {
  count: number
  at: string
  stale: boolean
}

// ChangeRow is the part of a change the page reads: one line per verdict, the
// overlap count for the radar, and the watcher's last poll and error.
// TestHealth is how many tests went over the room's per-test limit in their
// latest stored run, and the limit itself.
export interface TestHealth {
  count: number
  limitMs: number
}

export interface ChangeRow {
  key?: string
  reviewAt?: string
  number?: number
  title?: string
  verdict?: string
  overlaps?: { length: number } | number
  reviewerSession?: string
  kind?: string
  // watching is true while the watcher polls this change; checked is when it
  // last polled and error is why that poll could not finish.
  watching?: boolean
  checked?: string
  error?: string
}

// SyncRemote is one local lane remote as lane sync last saw it.
export interface SyncRemote {
  checkedAt: string
  lastError: string
}

// SyncInfo is what the page reads for one repository from the lane sync status:
// each local lane remote's last check, and the error of an export that failed.
export interface SyncInfo {
  remotes: SyncRemote[]
  operationError: string
}

// SyncInfoByRepo is the lane sync status keyed by the repository's path, as the
// engine stores it. The page shows the row for the room's own repository.
export type SyncInfoByRepo = Record<string, SyncInfo>

export interface Inputs {
  reviewer: ReviewerInfo | null
  queue: QueueRow[] | null
  mergePaused: boolean | null
  history: MergeHistoryRow[]
  // registry is the engine's service list, the one the CLI prints; null when it could not be read.
  registry: RegistryService[] | null
  services: ServiceInfo[] | null
  notices: NoticeRow[] | null
  hooks: HookRow[] | null
  // runs holds each hook's recent runs by hook name; a hook with none listed
  // falls back to its newest run. reasons gives a failed run's log reason by
  // run id.
  runs: Record<string, HookRunRow[]>
  reasons: Record<string, RunReason>
  settings: Record<string, string> | null
  budget: BudgetInfo | null
  accounts: AccountsInfo | null
  usage: UsageRow[] | null
  voice: string | null
  changes: ChangeRow[]
  testHealth: TestHealth | null
  laneSync: SyncInfoByRepo | null
  // deferred is when the engine's librarian wake is due; null when none is deferred.
  deferred: string | null
  repo: string
}

// Row is one service. A switch names the existing call it uses; the page maps
// the id to that call. profile is the stored choice, with "" meaning Auto.
export interface Row {
  id: string
  name: string
  state: string
  tone: Tone
  // detail is the plain status line under the switch: what the service is
  // doing, or why it has no switch.
  detail: string
  toggle: { id: string; on: boolean } | null
  profile: { id: string; value: string; options: string[] } | null
  value: string
  last: string
  nextAt: string
  nextText: string
  filterable: boolean
  settings?: { id: string; label: string; value: string; options?: string[] }[]
}

const unknown = 'unknown'

// readableDuration turns a stored duration such as "336h0m0s" into "14 days",
// and a short one such as "3m0s" into "3 min".
export function readableDuration(text: string): string {
  if (text === 'off') return 'off'
  let seconds = 0
  for (const [, n, unit] of text.matchAll(/(\d+(?:\.\d+)?)(h|m|s)/g)) seconds += Number(n) * (unit === 'h' ? 3600 : unit === 'm' ? 60 : 1)
  if (!seconds) return text === '' ? unknown : /^0(?:h|m|s)/.test(text) ? 'off' : text
  const [amount, unit, plural] = seconds % 86400 === 0 ? [seconds / 86400, 'day', 's'] : seconds % 3600 === 0 ? [seconds / 3600, 'hour', 's'] : seconds % 60 === 0 ? [seconds / 60, 'min', ''] : [seconds, 'second', 's']
  return `${amount} ${unit}${amount === 1 ? '' : plural}`
}

// shortDuration keeps the CI budget compact, with spaces between values and units.
export function shortDuration(text: string): string {
  const m = text.match(/^(?:(\d+)h)?(?:(\d+)m)?(?:(\d+)s)?$/)
  if (!m || text === '') return text
  const [, h, min, sec] = m
  return [Number(h) ? `${Number(h)} h` : '', Number(min) ? `${Number(min)} min` : '', Number(sec) ? `${Number(sec)} s` : ''].filter(Boolean).join(' ') || '0 s'
}

// hhmm is a stored time as a local clock time, such as "14:05". A watcher
// polls each minute, so the clock time says when its last tick ran.
export function hhmm(iso: string): string {
  const d = new Date(iso)
  if (!Number.isFinite(d.getTime())) return ''
  return `${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`
}

// seen drops an unset time. The engine sends an unset time as the zero time
// "0001-01-01T00:00:00Z", not as an empty string.
const seen = (iso?: string): string => (iso && new Date(iso).getFullYear() > 1 ? iso : '')

// msDuration names a millisecond limit in words: 1000 as "1 s", 10000 as "10 s".
export function msDuration(ms: number): string {
  const s = ms / 1000
  return s >= 60 && s % 60 === 0 ? `${s / 60} min` : `${s} s`
}

// testLine is the one plain line above the timeline, or nothing when no test is
// over the limit, or when the read did not answer.
export function testLine(health: TestHealth | null): string {
  if (!health || health.count <= 0) return ''
  return `${health.count} ${health.count === 1 ? 'test' : 'tests'} over ${msDuration(health.limitMs)}`
}

// elapsed is a run's length, as "36 s" or "6 m 20 s".
export function elapsed(from: string, to: string): string {
  const ms = new Date(to).getTime() - new Date(from).getTime()
  if (!Number.isFinite(ms) || ms < 0) return ''
  const s = Math.round(ms / 1000)
  return s < 60 ? `${s} s` : `${Math.floor(s / 60)} m ${s % 60} s`
}

// turnsOf is how many turns a role ran today and what they cost. Plan models
// have no price, so the page shows turns instead of an unknown price.
function turnsOf(usage: UsageRow[] | null, role: string): string {
  const rows = (usage ?? []).filter((u) => u.role === role)
  if (!rows.length) return ''
  const turns = rows.reduce((n, u) => n + u.turns, 0)
  if (rows.some((u) => u.usd === null || u.incomplete)) return turns ? `${turns} ${turns === 1 ? 'turn' : 'turns'}` : ''
  return '$' + rows.reduce((sum, u) => sum + (u.usd ?? 0), 0).toFixed(2)
}

function hook(rows: HookRow[] | null, name: string): HookRow | null {
  return rows?.find((r) => r.hook.name === name) ?? null
}

// queueCounts groups the merge queue by state. Drafts held by GitHub are not
// queue work, so they count apart from the states that need a worker.
export function queueCounts(queue: QueueRow[] | null): { running: number; ready: number; failed: number; blocked: number; held: number } {
  const counts = { running: 0, ready: 0, failed: 0, blocked: 0, held: 0 }
  const trials = new Set<string>()
  for (const q of queue ?? []) {
    switch (q.state) {
      case 'trial-running':
        if (q.trial?.branch) trials.add(q.trial.branch)
        else counts.running++
        break
      case 'ready': counts.ready++; break
      case 'ci-failed': counts.failed++; break
      case 'held': counts.held++; break
      default: counts.blocked++
    }
  }
  return { ...counts, running: counts.running + trials.size }
}

// queueLine says what the queue is doing in one phrase.
export function queueLine(queue: QueueRow[] | null): string {
  if (!queue) return 'The queue could not be read'
  if (!queue.length) return 'No open changes'
  const c = queueCounts(queue)
  const parts = [`${c.running} ${c.running === 1 ? 'trial' : 'trials'} running`, `${c.ready} ready`]
  if (c.failed) parts.push(`${c.failed} CI failed`)
  if (c.blocked) parts.push(`${c.blocked} waiting`)
  if (c.held) parts.push(`${c.held} held by drafts`)
  return parts.join(' · ')
}

// rateLimit reports whether a provider limit affects a service's profile. The
// page shows it as plain status, not as an alert.
export function rateLimit(notices: NoticeRow[] | null, profile: string): string {
  if (!notices || !profile) return ''
  return notices.some((n) => n.key === 'provider:' + profile && /limit/i.test(n.text)) ? 'rate-limited' : ''
}

// nextAt is when a scheduled hook runs next: its last run plus its interval.
export function nextAt(row: HookRow | null): string {
  const minutes = row?.hook.everyMinutes ?? 0
  const last = row?.lastRun?.startedAt
  if (!row || row.hook.disabled || row.hook.event !== 'schedule' || minutes <= 0 || !last) return ''
  const at = Date.parse(last) + minutes * 60000
  return Number.isFinite(at) ? new Date(at).toISOString() : ''
}

// A repository has a librarian only when its own path is enabled.
function libraryRepos(settings: Inputs['settings']): string[] | null {
  if (!settings) return null
  try {
    const value: unknown = JSON.parse(settings['library.enabled'] || '[]')
    return Array.isArray(value) && value.every((r) => typeof r === 'string') ? value : null
  } catch { return null }
}

// stateTone is how a registry state draws: working is live, an enabled service
// is on, a stopped or unconfigured one is off.
const stateTone: Record<RegistryState, Tone> = { on: 'on', running: 'live', waiting: 'on', idle: 'on', off: 'off', error: 'error', 'not-set-up': 'off' }

// detailLine words a registry service's typed detail for the owner. A service
// with no detail says what it does.
function detailLine(s: RegistryService): string {
  switch (s.detail) {
    case 'last-run-failed': return 'Last run failed'
    case 'no-run-yet': return 'No run yet'
    case 'profile-waiting': return 'Waiting for a profile that can run'
    case 'working': return s.count ? `Working · ${s.count} waiting` : 'Working'
    case 'queued': return `${s.count} waiting`
    case 'paused': return 'Paused'
    case 'in-a-call': return 'In a call'
    case 'unavailable': return 'Not available on this machine'
    case 'unreadable': return 'Could not be read'
    case '': return s.description
  }
}

export function buildRows(i: Inputs): Row[] {
  const svc = (role: string): ServiceInfo | undefined => i.services?.find((s) => s.role === role)
  const rows: Row[] = []
  const base = { toggle: null, profile: null, value: '', last: '', nextAt: '', nextText: '', filterable: true }
  // The engine's service list gives each row its state, detail and times, so
  // this page and `dyna services` always agree. Without it a row says unreadable.
  const reg = (id: RegistryServiceId): Pick<Row, 'state' | 'tone' | 'detail' | 'last' | 'nextAt'> => {
    const s = i.registry?.find((r) => r.id === id)
    return s
      ? { state: s.state, tone: stateTone[s.state], detail: detailLine(s), last: s.lastRun ?? '', nextAt: s.nextRun ?? '' }
      : { state: unknown, tone: 'off', detail: 'The service list could not be read', last: '', nextAt: '' }
  }

  // PR reviewers: one session per ready change, with a switch and a profile
  // the owner can change without opening anything.
  const rev = i.reviewer
  const reviewerSvc = svc('reviewer')
  rows.push({
    ...base,
    ...reg('reviewer'),
    id: 'reviewer',
    name: 'PR reviewers',
    toggle: rev ? { id: 'review.enabled', on: rev.enabled } : null,
    profile: reviewerSvc ? { id: 'review.profile', value: reviewerSvc.pinned, options: ['', ...reviewerSvc.profiles] } : null,
    value: turnsOf(i.usage, 'reviewer'),
  })

  // Merge queue: the worker runs trials and publishes; the switch pauses it.
  const paused = i.mergePaused
  rows.push({
    ...base,
    ...reg('merge-queue'),
    id: 'merge',
    name: 'Merge queue',
    toggle: i.queue && paused !== null ? { id: 'merge.paused', on: !paused } : null,
  })

  // Conflict radar runs with the watcher on every push, so it has no switch.
  // The registry counts it with the PR watcher, so its overlap line stays here.
  const overlaps = i.changes.filter((c) => (typeof c.overlaps === 'number' ? c.overlaps : c.overlaps?.length ?? 0) > 0).length
  rows.push({
    ...base,
    id: 'radar',
    name: 'Conflict radar',
    state: 'on',
    tone: 'on',
    detail: overlaps ? `${overlaps} open ${overlaps === 1 ? 'change overlaps' : 'changes overlap'} another` : 'No overlaps between open changes',
    nextText: 'each push',
  })

  // PR watcher and lane sync have no switch, because the review and merge flow
  // and the PR gates read what they fetch.
  rows.push({ ...base, ...reg('pr-watcher'), id: 'watcher', name: 'PR watcher', nextText: 'each minute' })
  rows.push({ ...base, ...reg('lane-sync'), id: 'lanesync', name: 'Lane sync', nextText: 'every 5 min' })

  // Librarian activation stays local; the browser shows status and profile.
  const library = svc('librarian')
  rows.push({
    ...base,
    ...reg('library'),
    id: 'librarian',
    name: 'Librarian',
    profile: library
      ? { id: 'library.profile', value: library.pinned, options: ['', ...library.profiles] }
      : i.settings ? { id: 'library.profile', value: i.settings['library.profile'] ?? '', options: [] } : null,
    value: turnsOf(i.usage, 'librarian'),
  })

  // Update Dyna follows its configured trigger, usually main-updated.
  const update = hook(i.hooks, 'self-update')
  const updateRunning = update?.lastRun?.result === 'pending'
  rows.push({
    ...base,
    ...reg('updates'),
    id: 'update',
    name: 'Update Dyna',
    toggle: update ? { id: 'update.enabled', on: !update.hook.disabled } : null,
    value: update && !updateRunning ? elapsed(update.lastRun?.startedAt ?? '', update.lastRun?.finishedAt ?? '') : '',
  })

  // Maintenance and account usage run on their own. They carry no switch,
  // because off would leave data on disk forever, or gain nothing at all.
  rows.push({
    ...base,
    ...reg('maintenance'),
    id: 'maintenance',
    name: 'Maintenance',
    nextText: 'each min',
    settings: i.settings ? [
      { id: 'runs.retention', label: 'Keep runs for', value: i.settings['runs.retention'] ?? '' },
      { id: 'worktrees.retention', label: 'Keep worktrees', value: i.settings['worktrees.retention'] ?? 'changed', options: ['changed', 'all', 'none'] },
    ] : [],
  })
  rows.push({ ...base, ...reg('account-usage'), id: 'usage', name: 'Account usage', nextText: 'on sign-in' })

  // CI admission decides what a run may cost; its budget is its control.
  rows.push({
    ...base,
    id: 'ci',
    name: 'CI admission',
    state: 'on',
    tone: 'on',
    detail: i.budget
      ? `Warn after ${shortDuration(i.budget.warnAfter)} · fail after ${shortDuration(i.budget.failAfter)} · tests ${shortDuration(i.budget.testMax)}`
      : 'Budget could not be read',
    nextText: 'each run',
  })

  // Voice runs one call at a time, started and stopped per call.
  rows.push({ ...base, ...reg('voice'), id: 'voice', name: 'Voice' })
  return rows
}

// until turns a future time into "in 6 m", "in 2 h" or "due".
export function until(iso: string, now: number): string {
  const minutes = Math.round((new Date(iso).getTime() - now) / 60000)
  if (!Number.isFinite(minutes)) return ''
  return minutes <= 0 ? 'due' : minutes < 60 ? `in ${minutes} m` : `in ${Math.round(minutes / 60)} h`
}
