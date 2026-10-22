// The palette's go-to rows: the open room's sessions, workflow runs and pull
// requests. They show only once the owner types, so the empty palette stays
// a short list of rooms and commands.
import { matches } from './palette.js'
import { changeName, changeTitle } from '../prs/pr.ts'
import { prRef } from '../prs/prlink.ts'

export interface Session { id: string; name: string; owner?: boolean; closed?: boolean; status?: string; profile?: string; role?: string }
export interface Run { id: string; name: string; status: string; parent?: string }
export interface Change { changeId: string; kind?: string; number?: number; title?: string; branch?: string; repo?: string; state?: string }

export type Target =
  | { kind: 'session'; key: string; label: string; hint: string; terms: string; session: Session }
  | { kind: 'run'; key: string; label: string; hint: string; terms: string; run: Run }
  | { kind: 'pr'; key: string; label: string; hint: string; terms: string; pr: string }

export const GROUPS: Record<Target['kind'], string> = { session: 'Sessions', run: 'Workflow runs', pr: 'Pull requests' }
// The most rows of each kind a query shows.
export const PER_KIND = 6

export function paletteTargets(sessions: Session[], runs: Run[], changes: Change[]): Target[] {
  return [
    ...sessions.filter((m) => !m.owner && m.id).map((m): Target => ({ kind: 'session', key: m.id, label: m.name, hint: m.closed ? 'closed' : m.status ?? '', terms: `${m.id} ${m.profile ?? ''} ${m.role ?? ''}`, session: m })),
    ...runs.map((r): Target => ({ kind: 'run', key: r.id, label: r.name, hint: r.parent ? `${r.status} · ${r.parent}` : r.status, terms: r.id, run: r })),
    ...changes.map((c): Target => ({ kind: 'pr', key: c.changeId, label: changeTitle(c), hint: changeName(c), terms: `${c.branch ?? ''} ${c.state ?? ''}`, pr: prRef(c) })),
  ]
}

// findTargets keeps the rows that match every word of the query, at most
// PER_KIND of each kind, in kind order. An empty query finds nothing.
export function findTargets(query: string, targets: Target[]): Target[] {
  if (!query.trim()) return []
  const seen: Record<string, number> = {}
  return targets.filter((t) => {
    if (!matches(query, t.label, t.hint, t.terms)) return false
    seen[t.kind] = (seen[t.kind] ?? 0) + 1
    return (seen[t.kind] ?? 0) <= PER_KIND
  })
}
