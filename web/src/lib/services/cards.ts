// The service cards on the Services page: one card per service, built from
// rows the app already loads and the existing engine calls. A field with no
// source stays empty and the page shows it as unknown; nothing is invented.

export type Tone = 'on' | 'running' | 'paused' | 'error'

export interface ServiceRow { role: string; profile: string; pinned: string; reason: string; profiles: string[] }
export interface ReviewerInfo { enabled: boolean; profile: string; lastError: string; current: { pr: number; title: string } | null; queued: number }
export interface QueueRow { state: string }
export interface HistoryRow { kind: string; outcome: string; at: string; changes: number; detail: string }
export interface ChangeRow { reviewAt?: string; overlaps?: number }
export interface JournalRow { at: string }
export interface UsageRow { role: string; usd: number | null; incomplete: boolean } // incomplete: some turns have no price, so usd is a lower bound
export interface MemberRow { role?: string; state?: string; closed?: boolean }

export interface ServiceInputs {
  now: number
  pending: string[] // data parts still loading
  services: ServiceRow[]
  reviewer: ReviewerInfo | null
  queue: QueueRow[] | null
  history: HistoryRow[] | null
  changes: ChangeRow[]
  journal: { at: string }[]
  // checkins is the entry journal: the keeper's own passes, newest first.
  checkins: JournalRow[]
  settings: Record<string, string> | null // null when the caller may not read settings
  usage: UsageRow[] | null
  voice: string | null // 'idle', 'live' or 'unavailable: why'; null when it cannot be read
  members: MemberRow[]
}

// A control names the existing call it uses; the page maps the id to that call.
export type Control =
  | { kind: 'toggle'; id: string; label: string; value: boolean }
  | { kind: 'select'; id: string; label: string; value: string; options: string[] }
  | { kind: 'text'; id: string; label: string; value: string }

export interface Card {
  id: string
  name: string
  tone: Tone
  status: string
  line: string
  profile: string // '' when the service has no profile
  last: string // an ISO time, or '' when unknown
  next: { at?: string; text?: string }
  cost: string
  controls: Control[]
}

const unknown = 'unknown'

// readableDuration turns a Go duration such as "336h0m0s" into "14 days".
// "off" and a zero duration mean the setting is off.
export function readableDuration(text: string): string {
  if (text === 'off') return 'off'
  let seconds = 0
  for (const [, n, unit] of text.matchAll(/(\d+(?:\.\d+)?)(h|m|s)/g)) seconds += Number(n) * (unit === 'h' ? 3600 : unit === 'm' ? 60 : 1)
  if (!seconds) return text === '' ? unknown : 'off'
  const [amount, unit] = seconds % 86400 === 0 ? [seconds / 86400, 'day'] : seconds % 3600 === 0 ? [seconds / 3600, 'hour'] : seconds % 60 === 0 ? [seconds / 60, 'minute'] : [seconds, 'second']
  return `${amount} ${unit}${amount === 1 ? '' : 's'}`
}

const newest = (times: (string | undefined)[]): string => times.filter((t): t is string => !!t).sort().at(-1) ?? ''

// costOf sums a role's spend today. It is unknown when any turn has no full
// price, and 'none today' when the role has no usage rows.
function costOf(usage: UsageRow[] | null, role: string): string {
  if (!usage) return unknown
  const rows = usage.filter((u) => u.role === role)
  if (!rows.length) return 'none today'
  if (rows.some((u) => u.usd === null || u.incomplete)) return unknown
  return '$' + rows.reduce((sum, u) => sum + (u.usd ?? 0), 0).toFixed(2)
}

function profileSelect(id: string, svc: ServiceRow | undefined): Control[] {
  return svc ? [{ kind: 'select', id, label: 'Profile', value: svc.pinned, options: ['', ...svc.profiles] }] : []
}

export function buildCards(i: ServiceInputs): Card[] {
  const svc = (role: string) => i.services.find((s) => s.role === role)
  const cards: Card[] = []
  // A card whose main data is still loading says so, rather than reading as an error.
  const loading = (id: string, name: string): Card => ({ id, name, tone: 'paused', status: 'loading', line: 'Loading…', profile: '', last: '', next: {}, cost: unknown, controls: [] })
  const push = (part: string, card: Card) => cards.push(i.pending.includes(part) ? loading(card.id, card.name) : card)

  const r = i.reviewer
  const reviewer = svc('reviewer')
  push('reviewer', {
    id: 'reviewer', name: 'PR reviewers',
    tone: !r ? 'error' : !r.enabled ? 'paused' : r.lastError ? 'error' : r.current ? 'running' : 'on',
    status: !r ? 'unreadable' : !r.enabled ? 'off' : r.lastError ? 'error' : r.current ? 'running' : 'on',
    line: !r ? 'Reviewer state could not be read' : r.lastError || (r.current ? `Reviewing #${r.current.pr} · ${r.current.title}` : r.queued ? `${r.queued} waiting for review` : 'Waiting for a PR'),
    profile: reviewer?.profile ?? '',
    last: newest(i.changes.map((c) => c.reviewAt)),
    next: r?.queued ? { text: `${r.queued} in queue` } : {},
    cost: costOf(i.usage, 'reviewer'),
    controls: r ? [{ kind: 'toggle', id: 'review.enabled', label: 'Review on', value: r.enabled }, ...profileSelect('profile.reviewer', reviewer)] : [],
  })

  const paused = !!i.queue?.some((q) => q.state === 'merge-paused')
  const last = i.history?.[0]
  push('queue', {
    id: 'merge', name: 'Merge queue and trials',
    tone: !i.queue ? 'error' : paused ? 'paused' : 'on',
    status: !i.queue ? 'unreadable' : paused ? 'paused' : 'on',
    line: !i.queue ? 'Queue could not be read' : `${i.queue.length} in queue${last ? ` · last ${last.kind} ${last.outcome}` : ''}`,
    profile: '', last: last?.at ?? '', next: i.queue?.length && !paused ? { text: 'when a PR is ready' } : {}, cost: unknown, controls: [],
  })

  const clashes = i.changes.filter((c) => (c.overlaps ?? 0) > 0).length
  cards.push({
    id: 'radar', name: 'Conflict radar', tone: 'on', status: 'on',
    line: clashes ? `${clashes} open ${clashes === 1 ? 'change overlaps' : 'changes overlap'} another` : 'No clashes',
    profile: '', last: '', next: { text: 'on each push' }, cost: unknown, controls: [],
  })

  const keeper = svc('keeper')
  const keeperLive = i.members.some((m) => m.role === 'keeper' && !m.closed && m.state === 'live')
  // Only the keeper's own passes move this clock; holder notes and item events must not.
  const check = i.checkins[0]?.at ?? ''
  push('services', {
    id: 'keeper', name: 'Goal keeper', tone: keeperLive ? 'running' : 'on', status: keeperLive ? 'running' : 'on',
    line: keeperLive ? 'Writing the digest' : 'Idle between digests',
    profile: keeper?.profile ?? '', last: check,
    // The engine schedules from when a round starts, and the journal is written when replies arrive, so the next run cannot be derived from the journal.
    next: {},
    cost: costOf(i.usage, 'keeper'),
    controls: profileSelect('profile.keeper', keeper),
  })

  const libEnabled = !!i.settings && (i.settings['library.enabled'] ?? '[]') !== '[]' && (i.settings['library.enabled'] ?? '') !== ''
  push('settings', {
    id: 'librarian', name: 'Librarian', tone: !i.settings ? 'paused' : libEnabled ? 'on' : 'paused', status: !i.settings ? unknown : libEnabled ? 'on' : 'off',
    line: !i.settings ? 'Settings need admin access' : libEnabled ? 'Keeps the feature map current' : 'Not enabled for any repository',
    profile: i.settings?.['library.profile'] ?? '', last: '', next: {}, cost: costOf(i.usage, 'librarian'),
    controls: i.settings ? [{ kind: 'text', id: 'library.profile', label: 'Profile', value: i.settings['library.profile'] ?? '' }] : [],
  })

  push('settings', {
    id: 'maintenance', name: 'Maintenance and retention', tone: 'on', status: 'on',
    line: i.settings ? `Runs kept ${readableDuration(i.settings['runs.retention'] ?? '')} · worktrees: ${i.settings['worktrees.retention'] ?? unknown}` : 'Settings need admin access',
    profile: '', last: '', next: {}, cost: unknown,
    controls: i.settings ? [
      { kind: 'text', id: 'runs.retention', label: 'Keep runs for', value: i.settings['runs.retention'] ?? '' },
      { kind: 'select', id: 'worktrees.retention', label: 'Keep worktrees', value: i.settings['worktrees.retention'] ?? 'changed', options: ['changed', 'all', 'none'] },
    ] : [],
  })

  const v = i.voice
  const off = v?.startsWith('unavailable') ?? false
  push('voice', {
    id: 'voice', name: 'Voice service', tone: v === null ? 'error' : off ? 'paused' : v === 'live' ? 'running' : 'on',
    status: v === null ? 'unreadable' : off ? 'off' : v === 'live' ? 'running' : 'on',
    line: v === null ? 'Voice status could not be read' : off ? v.replace(/^unavailable:?\s*/, '') || 'Not available' : v === 'live' ? 'A call is in progress' : 'No call in progress',
    profile: '', last: '', next: {}, cost: unknown, controls: [],
  })
  return cards
}

