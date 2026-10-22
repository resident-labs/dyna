import { trimHistoryRows } from '../ui/historyWindow.js'

const SESSION_WINDOW = 18
const time = (value?: string): number => Math.max(0, Date.parse(value ?? '') || 0)

// The fields of a transcript page this merge reads; Session has them all.
export interface PageTurn {
  n: number
  ended?: string
}
export interface PagePost {
  id: string
  ts?: number
}
export interface SessionPage {
  member?: { id?: string }
  turns?: PageTurn[] | null
  posts?: PagePost[] | null
  earlier?: boolean
  newer?: boolean
  cursor?: string
}

// A DM and its thread share this window. Background refreshes cannot bridge a
// discarded newer page; explicit forward reads or present restore that side.
export function mergeSessionPage<S extends SessionPage>(current: S | null | undefined, page: S, direction: boolean | string = false, reading = false): S {
 if (!current || current.member?.id !== page.member?.id || direction === 'present') return page
 const older = direction === true || direction === 'older'
 const refresh = !older && direction !== 'newer'
 const frozen = refresh && (current.newer || reading)
 const currentTurns = current.turns ?? []
 const currentPosts = current.posts ?? []
 const merge = <R>(before: R[], after: R[], key: (row: R) => string | number): R[] => [...new Map([...before, ...after].map((row): [string | number, R] => [key(row), row])).values()]
 const incomingTurns = frozen
  ? (page.turns ?? []).filter(t => currentTurns.some(old => old.n === t.n)) : page.turns ?? []
 const incomingPosts = frozen
  ? (page.posts ?? []).filter(p => currentPosts.some(old => old.id === p.id)) : page.posts ?? []
 const all = merge(currentTurns, incomingTurns, t => t.n).sort((a,b) => a.n-b.n)
 const { rows: turns, before, after }: { rows: PageTurn[]; before: number; after: number } = trimHistoryRows(all, SESSION_WINDOW, older)
 let posts = older
  ? merge(incomingPosts,currentPosts,p=>p.id)
  : merge(currentPosts,incomingPosts,p=>p.id)
 const since = before ? time(all[before-1]?.ended) : 0
 const until = after ? time(turns.at(-1)?.ended) : 0
 if (since || until) posts = posts.filter(p => (!since || (p.ts ?? 0) > since) && (!until || (p.ts ?? 0) <= until))
 const first = turns[0]
 return { ...current, ...page, turns, posts,
  earlier: before > 0 || (older ? page.earlier : current.earlier),
  newer: after > 0 || (frozen && ((page.turns ?? []).some(t => !currentTurns.some(old => old.n === t.n)) || (page.posts ?? []).some(p => !currentPosts.some(old => old.id === p.id)))) || (older || refresh ? current.newer : page.newer),
  cursor: first ? String(first.n) : current.cursor,
 }
}
