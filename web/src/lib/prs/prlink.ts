// The address of one open pull request: #room=<id>&pr=<number or changeId>&tab=<tab>.
// A GitHub PR goes by its number, a local change by its changeId. The
// conversation is the default tab, so its link leaves the tab out.
import { isLocal, type Facts } from './pr.ts'

export const PR_TABS = ['conversation', 'commits', 'checks', 'files']

// A link from the address bar: a PR with its tab, or an issue.
export interface Link {
  roomId?: string
  pr?: string
  tab?: string
  issue?: number
}

export function prHash(roomId: string, pr: string | number, tab = 'conversation', session = ''): string {
  const q = new URLSearchParams({ room: roomId, pr: String(pr) })
  if (session) q.set('session', session)
  if (tab !== 'conversation') q.set('tab', tab)
  return `#${q}`
}

export function parsePRHash(hash?: string | null): { roomId: string; pr: string; tab: string } | null {
  const q = new URLSearchParams((hash || '').replace(/^#/, ''))
  const pr = q.get('pr')
  if (!pr) return null
  const tab = q.get('tab')
  return { roomId: q.get('room') || '', pr, tab: tab !== null && PR_TABS.includes(tab) ? tab : 'conversation' }
}

// prRef is the link name of a row; findPR finds the row a link names.
export const prRef = (c: Facts): string => (!isLocal(c) && c.number ? String(c.number) : (c.changeId ?? ''))
export const findPR = <T extends Facts>(rows: T[] | null | undefined, pr: string): T | null => (rows ?? []).find((c) => prRef(c) === pr) ?? null

// lookupNumber is the number of a linked GitHub PR that the loaded rows do
// not hold, such as an old or closed PR, so the screen can load it; else 0.
export function lookupNumber(rows: Facts[] | null | undefined, link?: { pr?: string } | null): number {
  const pr = link?.pr ?? ''
  return /^\d+$/.test(pr) && !findPR(rows, pr) ? Number(pr) : 0
}

// Issues share the PR address format, with their own item key.
export function issueHash(roomId: string, issue: string | number): string {
  return '#' + new URLSearchParams({ room: roomId, issue: String(issue) })
}

export function parseIssueHash(hash?: string | null): { roomId: string; issue: number } | null {
  const q = new URLSearchParams((hash || '').replace(/^#/, ''))
  const issue = q.get('issue')
  return /^[1-9]\d*$/.test(issue ?? '') && Number.isSafeInteger(Number(issue))
    ? { roomId: q.get('room') || '', issue: Number(issue) }
    : null
}

// A file in code > Files: #room=<id>&file=<path>, plus &at=<commit> while an
// old version of the file is shown. If the file had another path then, &was=<that path>.
export function fileHash(roomId: string, file: string, at = '', was = '', session = ''): string {
  const q = new URLSearchParams({ room: roomId, file })
  if (session) q.set('session', session)
  if (at) q.set('at', at)
  if (at && was) q.set('was', was)
  return `#${q}`
}

export function parseFileHash(hash?: string | null): { roomId: string; file: string; at: string; was: string } | null {
  const q = new URLSearchParams((hash || '').replace(/^#/, ''))
  const file = q.get('file')
  return file ? { roomId: q.get('room') || '', file, at: q.get('at') || '', was: q.get('at') ? q.get('was') || '' : '' } : null
}
