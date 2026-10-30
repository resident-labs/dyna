// Helpers for the PRs and issues view (GitHub.svelte): matching a GitHub
// row to the PR watcher's record, the state badge, label colors and the
// order of a conversation.
import type { AuthorNotes, ChangeDetail, Finding, ReviewRound } from '../api/change.ts'
import type { GitHubCommit, GitHubDetail, GitHubItem, GitHubUser } from '../api/github.ts'
import { isRecord } from '../api/guards.ts'
import type { Facts } from './pr.ts'

export interface Tone {
  id: string
  label: string
  tone: string
}

// trackedFor finds the watcher's record for PR number in repo. A record
// that still watches wins over an old untracked one.
export function trackedFor<T extends Facts>(tracked: T[] | null | undefined, repo: string | null | undefined, number: number | undefined): T | null {
  const r = (repo || '').toLowerCase()
  const hits = (tracked ?? []).filter((p) => p.number === number && (!r || !p.repo || p.repo.toLowerCase() === r))
  return hits.find((p) => p.status !== 'untracked') ?? hits[0] ?? null
}

// itemState gives the badge for a PR or issue: id, label and tone. Tones
// follow GitHub's meaning in the app's colors.
export function itemState(kind: string, it?: Pick<Partial<GitHubItem>, 'state' | 'mergedAt' | 'isDraft' | 'stateReason'> | null): Tone {
  const s = (it?.state || '').toUpperCase()
  if (kind === 'pr') {
    if (s === 'MERGED' || it?.mergedAt) return { id: 'merged', label: 'Merged', tone: 'iris' }
    if (s === 'CLOSED') return { id: 'closed', label: 'Closed', tone: 'coral' }
    if (it?.isDraft) return { id: 'draft', label: 'Draft', tone: 'faint' }
    return { id: 'open', label: 'Open', tone: 'sage' }
  }
  if (s === 'OPEN') return { id: 'open', label: 'Open', tone: 'sage' }
  if (it?.stateReason === 'NOT_PLANNED') return { id: 'skip', label: 'Not planned', tone: 'faint' }
  return { id: 'done', label: 'Closed', tone: 'iris' }
}

// issueGroups splits issue rows into the groups the engine names in each
// row's group. An older closed issue has no group and goes last, in Closed.
const ISSUE_GROUPS: [string, string][] = [['needs-owner', 'Needs an owner'], ['working', 'An agent is working'], ['has-pr', 'Has a PR open'], ['closed-today', 'Closed today']]
export function issueGroups(items: GitHubItem[]): { id: string; label: string; rows: GitHubItem[] }[] {
  const groups = ISSUE_GROUPS.map(([id, label]) => ({ id, label, rows: items.filter((i) => i.group === id) }))
  groups.push({ id: 'closed', label: 'Closed', rows: items.filter((i) => !ISSUE_GROUPS.some(([id]) => id === i.group)) })
  return groups.filter((g) => g.rows.length)
}

// issueChip is the one state chip of an issue row; a row that needs an owner has none, it has Start.
export function issueChip(it: GitHubItem): { tone: string; label: string } | null {
  if (it.group === 'working') return { tone: 'iris', label: 'Agent working' }
  const pr = it.linkedPRs?.find((p) => p.state === 'OPEN')
  if (it.group === 'has-pr' && pr) return { tone: 'sage', label: `PR #${pr.number} ${pr.isDraft ? 'draft' : pr.state.toLowerCase()}` }
  if (it.group === 'needs-owner') return null
  const st = itemState('issue', it)
  return { tone: '', label: st.label }
}

// labelStyle tints a label with its GitHub color, readable on a dark page.
export function labelStyle(hex?: string | null): string {
  const c = hex && /^[0-9a-f]{6}$/i.test(hex) ? hex : '8b8b8b'
  return `color: #${c}; background: #${c}22; box-shadow: inset 0 0 0 1px #${c}55; filter: brightness(1.25)`
}

const REVIEW: Record<string, { word: string; tone: string }> = {
  APPROVED: { word: 'approved', tone: 'sage' },
  CHANGES_REQUESTED: { word: 'requested changes', tone: 'gold' },
  COMMENTED: { word: 'reviewed', tone: 'quiet' },
  DISMISSED: { word: 'review dismissed', tone: 'quiet' },
}

// sortThread joins comments and reviews in time order. An empty COMMENTED
// review is only the shell of inline comments; it adds nothing here.
export interface ThreadEntry {
  author: GitHubUser
  body: string
  at: string
  url?: string
  review?: string
  tone?: string
}
export function sortThread(d?: Partial<Pick<GitHubDetail, 'comments' | 'reviews'>> | null): ThreadEntry[] {
  const out: ThreadEntry[] = (d?.comments ?? []).map((c) => ({ author: c.author, body: c.body, at: c.createdAt, url: c.url }))
  for (const r of d?.reviews ?? []) {
    if (r.state === 'COMMENTED' && !r.body?.trim()) continue
    if (r.state === 'PENDING') continue
    const m = REVIEW[r.state] ?? { word: r.state.toLowerCase(), tone: 'quiet' }
    out.push({ author: r.author, body: r.body, at: r.submittedAt, review: m.word, tone: m.tone, url: r.url })
  }
  return out.sort((a, b) => Date.parse(a.at) - Date.parse(b.at))
}

// timeline joins a PR's comments and reviewer rounds in time order. thread is
// the sorted comments from sortThread; rounds are the change's
// review rounds. Bots are not in it: they never block, so the screen shows
// them as notes.
export type TimelineEntry = { kind: 'post'; at: string; post: ThreadEntry } | { kind: 'round'; at: string; round: ReviewRound }
export function timeline(thread: ThreadEntry[] | null | undefined, rounds: ReviewRound[] | null | undefined): TimelineEntry[] {
  const at = (e: TimelineEntry) => Date.parse(e.at) || 0
  const posts = (thread ?? []).map((post): TimelineEntry => ({ kind: 'post', at: post.at, post }))
  const reviews = (rounds ?? []).map((round): TimelineEntry => ({ kind: 'round', at: round.at, round }))
  return [...posts, ...reviews].sort((a, b) => at(a) - at(b))
}

// openFindings are the findings of a round that nobody has settled: the author
// has not marked them fixed and the owner has not declined them. A question
// reply leaves a finding open.
export const openFindings = (round?: Pick<ReviewRound, 'findings'> | null): Finding[] => (round?.findings ?? []).filter((f) => !(f.replies ?? []).some((r) => r.disposition === 'fixed' || r.disposition === 'wont-fix'))

// firstSentence is the opening sentence of a review summary, as plain text.
export function firstSentence(summary?: string | null): string {
  const para = ((summary ?? '').trim().split(/\n\s*\n/)[0] ?? '').replace(/[*_`#>]/g, '').replace(/\s+/g, ' ').trim()
  return /^.*?[.!?](?=\s|$)/.exec(para)?.[0] ?? para
}

// roundReview is what ChangeReview shows for one round: that round's findings,
// with no rounds list. Replies only reach the latest findings, so an earlier
// round gets no roomId and its findings are read-only.
// ReviewChange is the change ChangeReview shows. A round other than the latest has no author notes.
export type ReviewChange = Omit<Partial<ChangeDetail>, 'notes'> & { notes?: Partial<AuthorNotes> }
export function roundReview(change: Partial<ChangeDetail> | null | undefined, round: ReviewRound, latest: Pick<ReviewRound, 'round'> | null | undefined, roomId: string): { change: ReviewChange; roomId: string } {
  const isLatest = round.round === latest?.round
  return { change: { ...change, findings: round.findings, reviewRounds: [], notes: isLatest ? change?.notes : {} }, roomId: isLatest ? roomId : '' }
}

// ownerReview is what ChangeReview shows for the owner's own comments. They
// live outside review rounds, so they show and resolve whatever the reviewer did.
export function ownerReview(change: Partial<ChangeDetail> | null | undefined, roomId: string): { change: ReviewChange; roomId: string } {
  return { change: { ...change, findings: (change?.findings ?? []).filter((f) => f.severity === 'owner'), reviewRounds: [], notes: {} }, roomId }
}

// The kind and state filters persist per browser, like the Wrap choice.
const KEY = 'dyna.github'
export function loadFilters(): { state: string } {
  try {
    const s: unknown = JSON.parse(localStorage.getItem(KEY) || '{}')
    const states = ['open', 'closed', 'all']
    return { state: isRecord(s) && typeof s.state === 'string' && states.includes(s.state) ? s.state : 'open' }
  } catch {
    return { state: 'open' }
  }
}
export function saveFilters(f: { state: string }): void {
  try {
    localStorage.setItem(KEY, JSON.stringify(f))
  } catch {}
}

// ListScope is what the PRs and Issues tabs list: what the room tracks, or
// everything open on GitHub. Each room remembers its own choice per tab, so a
// busy upstream room can stay on Tracked while a small room shows all.
export type ListScope = 'tracked' | 'all'
export type ListKind = 'pr' | 'issue'
const SCOPE_KEY = 'dyna.github.scope'
function savedScopes(): Record<string, unknown> {
  try {
    const s: unknown = JSON.parse(localStorage.getItem(SCOPE_KEY) || '{}')
    return isRecord(s) ? s : {}
  } catch {
    return {}
  }
}
export function loadScope(roomId: string, kind: ListKind): ListScope {
  const room = savedScopes()[roomId]
  return isRecord(room) && room[kind] === 'all' ? 'all' : 'tracked'
}
export function saveScope(roomId: string, kind: ListKind, scope: ListScope): void {
  const all = savedScopes()
  const room = all[roomId]
  try {
    localStorage.setItem(SCOPE_KEY, JSON.stringify({ ...all, [roomId]: { ...(isRecord(room) ? room : {}), [kind]: scope } }))
  } catch {}
}

// commitDays groups a PR's commits by local day for the Commits tab: the
// newest day first, and the newest commit first in a day. Ancestry does not
// order author dates, so it sorts by date rather than trusting the input.
export function commitDays<T extends Pick<GitHubCommit, 'date'>>(commits: T[] | null | undefined): { key: string; label: string; commits: T[] }[] {
  const days = new Map<string, { key: string; label: string; commits: T[] }>()
  for (const c of [...(commits ?? [])].sort((a, b) => Date.parse(b.date) - Date.parse(a.date))) {
    const d = new Date(c.date)
    const key = `${d.getFullYear()}-${d.getMonth()}-${d.getDate()}`
    const day = days.get(key) ?? { key, label: d.toLocaleDateString('en-US', { month: 'short', day: 'numeric', year: 'numeric' }), commits: [] }
    day.commits.push(c)
    days.set(key, day)
  }
  return [...days.values()]
}
