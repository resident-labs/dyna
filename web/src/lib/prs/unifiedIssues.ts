import type { GitHubItem } from '../api/github.ts'
import type { IssueState } from '../rooms/events.gen.ts'

export interface RoomIssue {
  id: string
  number: number
  title: string
  file?: string
  line?: number
  filer?: string
  filedAt: number
  state: IssueState
  reason?: string
  goalId?: string
  githubUrl?: string
  // fixPr is the open PR that closes this issue on merge.
  fixPr?: number
  inProgress?: boolean
  goalShared?: boolean
  goalHolder?: string
  changedAt?: number
}
export interface IssueReference { kind: 'pr' | 'issue' | 'room_issue'; number: number | string; title: string; state: string; url: string }
// A room issue shows its number as D-12. A row without one (an older engine)
// falls back to the message ID, like the Go Label.
export const roomIssueLabel = (i: RoomIssue): string => i.number > 0 ? `D-${i.number}` : i.id
export const issueReference = (i: IssueRow): IssueReference => ({ kind: i.number ? 'issue' : 'room_issue', number: i.number || (i.local ? roomIssueLabel(i.local) : ''), title: i.title, state: i.state, url: i.url })
export type IssueRow = GitHubItem & { local?: RoomIssue; source?: 'Dyna' | 'GitHub' }

export type IssueWork = 'unhandled' | 'in-progress' | 'other'

// issueWork is the view's projection of the backend's local issue rule and
// GitHub's own work facts. Older engines omit inProgress, so their legacy
// goal and PR fields remain a fallback.
export function issueWork(it: IssueRow): IssueWork {
  const local = it.local
  if (it.source === 'Dyna') {
    if (!local || local.state !== 'open' && local.state !== 'goal') return 'other'
    const progress = local.inProgress ?? (local.state === 'goal' || !!local.fixPr)
    return progress ? 'in-progress' : 'unhandled'
  }
  if (it.state !== 'OPEN') return 'other'
  return it.group === 'working' || it.group === 'has-pr' || it.workingSessionLive ? 'in-progress' : 'unhandled'
}

const urlKey = (url: string) => url.replace(/\/+$/, '').toLowerCase()

export function unifiedIssues(github: readonly GitHubItem[], local: readonly RoomIssue[], source: string, state: string, query: string): IssueRow[] {
  // Open and history requests can finish separately during a state change.
  const locals = [...new Map(local.map(i => [i.id, i])).values()]
  const exported = new Map(locals.filter(i => i.githubUrl).map(i => [urlKey(i.githubUrl || ''), i]))
  const seen = new Set(github.map(i => urlKey(i.url)))
  const rows: IssueRow[] = github.map(i => ({ ...i, source: 'GitHub', local: exported.get(urlKey(i.url)) }))
  // Export history does not tell us whether the linked GitHub issue is open or closed.
  // Unresolved exports appear only in All, until a GitHub response supplies their state.
  for (const i of locals) {
    if (i.githubUrl && seen.has(urlKey(i.githubUrl))) continue
    rows.push({ number: 0, title: i.title, state: i.githubUrl ? 'UNKNOWN' : i.state === 'open' || i.state === 'goal' ? 'OPEN' : 'CLOSED', group: i.state === 'open' ? 'needs-owner' : '', author: { login: i.filer || '' }, labels: [], workingSessionLive: false, createdAt: new Date(i.filedAt).toISOString(), updatedAt: new Date(i.changedAt || i.filedAt).toISOString(), url: i.githubUrl || '', source: 'Dyna', local: i, fixPR: i.fixPr || undefined })
  }
  const q = query.trim().toLowerCase()
  // People type D-12, d12 or 12, so both spellings are searchable. A number
  // query compares the number, so "12" does not find D-112.
  const wanted = q.match(/^#?d?-?(\d+)$/)?.[1]
  const haystack = (i: IssueRow) => {
    const label = i.local && i.local.number > 0 ? [roomIssueLabel(i.local), `d${i.local.number}`] : []
    // A number query must not match the label of another issue.
    return [...(wanted ? [] : label), i.title, i.local?.file || '', i.local?.filer || ''].join(' ').toLowerCase()
  }
  const matchesQuery = (i: IssueRow) => !q || i.source === 'GitHub' ||
    (wanted ? i.local?.number === Number(wanted) || haystack(i).includes(q) : haystack(i).includes(q))
  return rows.filter(i => (source === 'all' || i.source?.toLowerCase() === source) && (state === 'all' || i.state === state.toUpperCase()) && matchesQuery(i))
    .sort((a, b) => Number(b.state === 'OPEN') - Number(a.state === 'OPEN') || Date.parse(b.updatedAt) - Date.parse(a.updatedAt))
}
