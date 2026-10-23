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
  lastRun: { result: string; startedAt: string; finishedAt: string } | null
}

export interface MergeHistoryRow {
  kind: string
  outcome: string
  at: string
  changes: number
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
  mergePaused: boolean | null
  history: MergeHistoryRow[]
  services: ServiceInfo[] | null
  notices: NoticeRow[] | null
  hooks: HookRow[] | null
  settings: Record<string, string> | null
  budget: BudgetInfo | null
  accounts: AccountsInfo | null
  usage: UsageRow[] | null
  voice: string | null
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

export function buildRows(i: Inputs): Row[] {
  const svc = (role: string): ServiceInfo | undefined => i.services?.find((s) => s.role === role)
  const rows: Row[] = []
  const base = { profileStatus: '', toggle: null, profile: null, value: '', last: '', nextAt: '', nextText: '', filterable: true }

  // PR reviewers: one session per ready change, with a switch and a profile
  // the owner can change without opening anything.
  const rev = i.reviewer
  const reviewerSvc = svc('reviewer')
  rows.push({
    ...base,
    id: 'reviewer',
    name: 'PR reviewers',
    state: !rev ? 'unreadable' : !rev.enabled ? 'off' : rev.lastError ? 'error' : rev.current ? 'running' : 'waiting',
    tone: !rev ? 'off' : !rev.enabled ? 'off' : rev.lastError ? 'error' : rev.current ? 'live' : 'on',
    detail: !rev
      ? 'Reviewer state could not be read'
      : rev.lastError || (rev.current ? `reviewing #${rev.current.pr} · ${rev.current.title}` : rev.queued ? `${rev.queued} waiting for review` : 'nothing to review now'),
    profileStatus: rateLimit(i.notices, reviewerSvc?.profile ?? rev?.profile ?? ''),
    toggle: rev ? { id: 'review.enabled', on: rev.enabled } : null,
    profile: reviewerSvc ? { id: 'review.profile', value: reviewerSvc.pinned, options: ['', ...reviewerSvc.profiles] } : null,
    value: turnsOf(i.usage, 'reviewer'),
    last: rev?.lastVerdictAt ?? '',
    nextText: rev?.queued ? `${rev.queued} waiting` : '',
  })

  // Merge queue: the worker runs trials and publishes; the switch pauses it.
  const paused = i.mergePaused
  const counts = queueCounts(i.queue)
  rows.push({
    ...base,
    id: 'merge',
    name: 'Merge queue',
    state: !i.queue || paused === null ? 'unreadable' : paused ? 'paused' : counts.running ? 'running' : 'waiting',
    tone: !i.queue || paused === null ? 'off' : paused ? 'off' : counts.running ? 'live' : 'on',
    detail: queueLine(i.queue),
    toggle: i.queue && paused !== null ? { id: 'merge.paused', on: !paused } : null,
    last: i.history[0]?.at ?? '',
    nextText: counts.ready ? `${counts.ready} ready` : '',
  })

  // Merge steward: its profile is its switch, because off is the off state.
  const steward = svc('merge-steward')
  const stewardOff = steward?.pinned === 'off' || steward?.profile === 'off'
  rows.push({
    ...base,
    id: 'steward',
    name: 'Merge steward',
    state: !steward ? 'unreadable' : stewardOff ? 'off' : !steward.profile ? 'unavailable' : 'on',
    tone: !steward || stewardOff || !steward.profile ? 'off' : 'on',
    detail: !steward ? 'Steward state could not be read' : stewardOff ? 'Off: a failed batch goes straight to root' : !steward.profile ? steward.reason || 'No enabled profile' : 'Handles a failed merge batch',
    profile: steward ? { id: 'steward.profile', value: steward.pinned, options: ['', 'off', ...steward.profiles] } : null,
  })

  // Conflict radar runs with the watcher on every push, so it has no switch.
  const overlaps = i.changes.filter((c) => (typeof c.overlaps === 'number' ? c.overlaps : c.overlaps?.length ?? 0) > 0).length
  rows.push({
    ...base,
    id: 'radar',
    name: 'Conflict radar',
    state: 'always on',
    tone: 'on',
    detail: overlaps ? `${overlaps} open ${overlaps === 1 ? 'change overlaps' : 'changes overlap'} another` : 'No overlaps between open changes',
    value: overlaps ? String(overlaps) : '',
    nextText: 'each push',
  })

  // Librarian activation stays local; the browser shows status and profile.
  const repos = libraryRepos(i.settings)
  const libraryOn = repos === null || !i.repo ? null : repos.includes(i.repo)
  const batch = hook(i.hooks, 'librarian-batch')
  rows.push({
    ...base,
    id: 'librarian',
    name: 'Librarian',
    state: libraryOn === null ? unknown : libraryOn ? 'on' : 'off',
    tone: libraryOn === null ? 'off' : libraryOn ? 'on' : 'off',
    detail: libraryOn === null
      ? !i.repo ? 'No repository for this room' : 'Librarian settings could not be read'
      : libraryOn ? 'Keeps the feature map current' : 'Turn on with dyna library enable on this machine',
    profile: i.settings ? { id: 'library.profile', value: i.settings['library.profile'] ?? '', options: [] } : null,
    value: turnsOf(i.usage, 'librarian'),
    last: batch?.lastRun?.startedAt ?? '',
    nextAt: libraryOn ? nextAt(batch) : '',
  })

  // Update Dyna follows its configured trigger, usually main-updated.
  const update = hook(i.hooks, 'self-update')
  const updateOff = update?.hook.disabled ?? false
  const updateResult = update?.lastRun?.result ?? ''
  const updateFailed = updateResult === 'error'
  const updateRunning = updateResult === 'pending'
  const updateTrigger = !update ? '' : update.hook.event === 'schedule'
    ? `Every ${update.hook.everyMinutes} min`
    : update.hook.event === 'main-updated' ? 'When main updates' : `On ${update.hook.event.replaceAll('-', ' ')}`
  const updateLast = updateRunning ? 'running' : `last ${updateResult === 'pass' ? 'passed' : updateFailed ? 'failed' : updateResult || unknown}`
  rows.push({
    ...base,
    id: 'update',
    name: 'Update Dyna',
    state: !update ? (i.hooks === null ? unknown : 'off') : updateRunning ? 'running' : updateOff ? 'off' : updateFailed ? 'error' : 'on',
    tone: !update ? 'off' : updateRunning ? 'live' : updateOff ? 'off' : updateFailed ? 'error' : 'on',
    detail: i.hooks === null
      ? 'Hook state could not be read'
      : !update
        ? 'No update hook in this repository'
        : `${updateTrigger} · ${update.lastRun ? updateLast : 'no run yet'}`,
    toggle: update ? { id: 'update.enabled', on: !updateOff } : null,
    value: update && !updateRunning ? elapsed(update.lastRun?.startedAt ?? '', update.lastRun?.finishedAt ?? '') : '',
    last: update?.lastRun?.startedAt ?? '',
    nextAt: nextAt(update),
    nextText: update && !updateOff ? updateTrigger : '',
  })

  // Maintenance and account usage run on their own. They carry no switch,
  // because off would leave data on disk forever, or gain nothing at all.
  rows.push({
    ...base,
    id: 'maintenance',
    name: 'Maintenance',
    state: 'always on',
    tone: 'on',
    detail: i.settings ? `Keeps runs ${readableDuration(i.settings['runs.retention'] ?? '')} · worktrees ${i.settings['worktrees.retention'] ?? unknown}` : 'Settings need admin access',
    nextText: 'each min',
    settings: i.settings ? [
      { id: 'runs.retention', label: 'Keep runs for', value: i.settings['runs.retention'] ?? '' },
      { id: 'worktrees.retention', label: 'Keep worktrees', value: i.settings['worktrees.retention'] ?? 'changed', options: ['changed', 'all', 'none'] },
    ] : [],
  })
  rows.push({
    ...base,
    id: 'usage',
    name: 'Account usage',
    state: !i.accounts ? unknown : i.accounts.stale ? 'stale' : 'on',
    tone: !i.accounts ? 'off' : i.accounts.stale ? 'off' : 'on',
    detail: !i.accounts ? 'Account usage could not be read' : `Refreshes on sign-in and account switch · ${i.accounts.count} ${i.accounts.count === 1 ? 'account' : 'accounts'}`,
    last: i.accounts?.at ?? '',
    nextText: 'on sign-in',
  })

  // CI admission decides what a run may cost; its budget is its control.
  rows.push({
    ...base,
    id: 'ci',
    name: 'CI admission',
    state: 'always on',
    tone: 'on',
    detail: i.budget
      ? `Warn after ${shortDuration(i.budget.warnAfter)} · fail after ${shortDuration(i.budget.failAfter)} · tests ${shortDuration(i.budget.testMax)}`
      : 'Budget could not be read',
    nextText: 'each run',
  })

  // Voice runs one call at a time, started and stopped per call.
  const voiceOff = i.voice?.startsWith('unavailable') ?? false
  rows.push({
    ...base,
    id: 'voice',
    name: 'Voice',
    state: i.voice === null ? unknown : voiceOff ? 'off' : i.voice === 'live' ? 'in a call' : 'idle',
    tone: i.voice === null || voiceOff ? 'off' : i.voice === 'live' ? 'live' : 'on',
    detail: i.voice === null ? 'Voice status could not be read' : voiceOff ? i.voice.replace(/^unavailable:?\s*/, '') || 'Not available' : 'Started and stopped per call',
  })
  return rows
}

// Next is one thing that runs on a timer.
export interface Next {
  id: string
  name: string
  when: string
  at: number
}

// buildSchedule lists what runs next, soonest first. A row with a time comes
// before a row that only names its trigger.
export function buildSchedule(rows: Row[], now: number): Next[] {
  return rows
    .filter((r) => r.nextAt || r.nextText)
    .map((r) => ({ id: r.id, name: r.name, when: r.nextAt ? until(r.nextAt, now) : r.nextText, at: r.nextAt ? new Date(r.nextAt).getTime() : Number.POSITIVE_INFINITY }))
    .sort((a, b) => a.at - b.at || a.name.localeCompare(b.name))
}

// until turns a future time into "in 6 m", "in 2 h" or "due".
export function until(iso: string, now: number): string {
  const minutes = Math.round((new Date(iso).getTime() - now) / 60000)
  if (!Number.isFinite(minutes)) return ''
  return minutes <= 0 ? 'due' : minutes < 60 ? `in ${minutes} m` : `in ${Math.round(minutes / 60)} h`
}

