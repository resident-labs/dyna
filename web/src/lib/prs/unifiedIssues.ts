import type { GitHubItem } from '../api/github.ts'

export interface RoomIssue {
  id: string
  title: string
  file?: string
  line?: number
  filer?: string
  filedAt: number
  state: string
  reason?: string
  goalId?: string
  githubUrl?: string
  changedAt?: number
}
export interface IssueReference { kind: 'pr' | 'issue' | 'room_issue'; number: number | string; title: string; state: string; url: string }
export const issueReference = (i: IssueRow): IssueReference => ({ kind: i.number ? 'issue' : 'room_issue', number: i.number || i.local?.id || '', title: i.title, state: i.state, url: i.url })
export type IssueRow = GitHubItem & { local?: RoomIssue; source?: 'Dyna' | 'GitHub' }
const urlKey = (url: string) => url.replace(/\/+$/, '').toLowerCase()

export function unifiedIssues(github: readonly GitHubItem[], local: readonly RoomIssue[], source: string, state: string, query: string): IssueRow[] {
  // Open and history requests can finish separately during a state change.
  const locals = [...new Map(local.map(i => [i.id, i])).values()]
  const exported = new Map(locals.filter(i => i.githubUrl).map(i => [urlKey(i.githubUrl || ''), i]))
  const seen = new Set(github.map(i => urlKey(i.url)))
  const rows: IssueRow[] = github.map(i => ({ ...i, source: 'GitHub', local: exported.get(urlKey(i.url)) }))
  for (const i of locals) {
    if (i.githubUrl && seen.has(urlKey(i.githubUrl))) continue
    rows.push({ number: 0, title: i.title, state: i.state === 'open' ? 'OPEN' : 'CLOSED', group: i.state === 'open' ? 'needs-owner' : '', author: { login: i.filer || '' }, labels: [], workingSessionLive: false, createdAt: new Date(i.filedAt).toISOString(), updatedAt: new Date(i.changedAt || i.filedAt).toISOString(), url: i.githubUrl || '', source: 'Dyna', local: i })
  }
  const q = query.trim().toLowerCase()
  return rows.filter(i => (source === 'all' || i.source?.toLowerCase() === source) && (state === 'all' || (i.state === 'OPEN') === (state === 'open')) && (!q || i.source === 'GitHub' || (i.title + ' ' + (i.local?.file || '') + ' ' + (i.local?.filer || '')).toLowerCase().includes(q)))
    .sort((a, b) => Number(b.state === 'OPEN') - Number(a.state === 'OPEN') || Date.parse(b.updatedAt) - Date.parse(a.updatedAt))
}
