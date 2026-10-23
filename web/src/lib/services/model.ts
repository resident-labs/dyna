// The Services page's data model: one row per service with its switch and
// profile, the activity timeline, and what runs next. Every value comes from a
// call the engine already serves; a field with no source stays empty and the
// page shows it as unknown. Nothing here changes data.

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
  batch: number
}

export interface NoticeRow {
  key: string
  severity: string
  text: string
}

// HookRow is one HookSummary row: the hook and its newest run.
export interface HookRow {
  hook: { name: string; label: string; event: string; everyMinutes: number; disabled: boolean }
  lastRun: { result: string; startedAt: string; finishedAt: string } | null
}

export interface MergeHistoryRow {
  kind: string
  outcome: string
  at: string
  changes: number
}

export interface JournalRow {
  at: string
  event?: string
  text?: string
  goal?: string
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

// ChangeRow is the part of a change the page reads: one line per verdict and
// the overlap count for the radar.
// TestHealth is how many tests went over the room's per-test limit in their
// latest stored run, and the limit itself.
export interface TestHealth {
  count: number
  limitMs: number
}

export interface ChangeRow {
  reviewAt?: string
  number?: number
  title?: string
  verdict?: string
  overlaps?: { length: number } | number
  reviewerSession?: string
}

export interface Inputs {
  reviewer: ReviewerInfo | null
  queue: QueueRow[] | null
  history: MergeHistoryRow[]
  services: ServiceInfo[] | null
  notices: NoticeRow[] | null
  hooks: HookRow[] | null
  settings: Record<string, string> | null
  budget: BudgetInfo | null
  accounts: AccountsInfo | null
  usage: UsageRow[] | null
  voice: string | null
  journal: JournalRow[]
  changes: ChangeRow[]
  testHealth: TestHealth | null
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
  // profileStatus is plain status beside the profile, such as a rate limit.
  // It is never an alert: recovering from it is the engine's job.
  profileStatus: string
  toggle: { id: string; on: boolean } | null
  profile: { id: string; value: string; options: string[] } | null
  value: string
  last: string
  nextAt: string
  nextText: string
  filterable: boolean
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

// shortDuration keeps a stored duration as short as it can be: "3m0s" as "3m",
// "1s" as "1s". The page has little room for the CI budget line.
export function shortDuration(text: string): string {
  const m = text.match(/^(?:(\d+)h)?(?:(\d+)m)?(?:(\d+)s)?$/)
  if (!m || text === '') return text
  const [, h, min, sec] = m
  return [h ? `${Number(h)}h` : '', min && min !== '0' ? `${Number(min)}m` : '', sec ? `${Number(sec)}s` : ''].join('') || '0s'
}

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
  for (const q of queue ?? []) {
    switch (q.state) {
      case 'trial-running': counts.running++; break
      case 'ready': counts.ready++; break
      case 'ci-failed': counts.failed++; break
      case 'held': counts.held++; break
      default: counts.blocked++
    }
  }
  return counts
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

