import { api } from '../../api.ts'
import { isRecord, isString } from '../api/guards.ts'

export interface Reference {
  kind: 'pr' | 'issue' | 'room_issue'
  number: number | string
  title: string
  state: string
  url: string
}

export const referenceKey = (reference: Reference) => `${reference.kind}:${reference.number}`
// A room issue shows its per-room number as D-12. A stored message ID has no
// short label, so it shows only "Issue".
const roomIssueNumber = (number: number | string) => String(number).trim().toLowerCase().match(/^#?d-?([0-9]+)$/)?.[1]
export const referenceLabel = (reference: Reference) => {
  if (reference.kind !== 'room_issue') return `${reference.kind === 'pr' ? 'PR' : 'Issue'} #${reference.number}`
  const number = roomIssueNumber(reference.number)
  return number ? `Issue D-${number}` : 'Issue'
}

export function matchReferences(references: Reference[], query: string) {
  const search = query.replace(/^#/, '').trim().toLowerCase()
  // D-12, d12 and 12 all find the same room issue.
  const text = (reference: Reference) => {
    const number = reference.kind === 'room_issue' ? roomIssueNumber(reference.number) : ''
    return [reference.number, number && `D-${number}`, number && `d${number}`, reference.title].filter(Boolean).join(' ').toLowerCase()
  }
  return references.filter((reference) => text(reference).includes(search))
    .sort((a, b) => Number(b.state.toLowerCase() === 'open') - Number(a.state.toLowerCase() === 'open'))
    .slice(0, 12)
}

// Row is the part of a GitHub row or a room issue that a reference shows.
interface Row {
  number: number | string
  title: string
  state: string
  url: string
}

function isRow(v: unknown): v is Row {
  return isRecord(v) && (typeof v.number === 'number' || isString(v.number)) && isString(v.title) && isString(v.state) && isString(v.url)
}

// rows reads a list answer's items, or a single row answer, as rows.
function rows(v: unknown, list: boolean): Row[] {
  if (!list) return isRow(v) ? [v] : []
  if (!isRecord(v) || !Array.isArray(v.items)) return []
  return v.items.filter(isRow)
}

// number is absent on an engine older than the D-N change. In Go it does not
// exist at all, so it is optional here.
function isRoomIssue(v: unknown): v is { id: string; number?: number; title: string; state: string } {
  return isRecord(v) && isString(v.id) && (v.number === undefined || typeof v.number === 'number') && isString(v.title) && isString(v.state)
}

// issueRows reads the room's issues, whose number is shown as D-12.
function issueRows(v: unknown, roomId: string): Row[] {
  if (!Array.isArray(v)) return []
  return v.filter(isRoomIssue).map((it) => ({ number: it.number ? `D-${it.number}` : it.id, title: it.title, state: it.state, url: `#room=${encodeURIComponent(roomId)}&view=issues` }))
}

export async function loadReferences(roomId: string, query = ''): Promise<Reference[]> {
  const state = query ? 'all' : 'open'
  const search = query && !/^\d+$/.test(query) ? `${query} in:title` : query
  // Each lookup answers a list, or one row for a numbered search.
  const lookups: { kind: Reference['kind']; request: Promise<unknown> }[] = [
    { kind: 'pr', request: api.githubList(roomId, 'pr', state, search) },
    { kind: 'issue', request: api.githubList(roomId, 'issue', state, search) },
    { kind: 'room_issue', request: api.issues(roomId, true) },
  ]
  // GitHub text search does not reliably find a row by its number.
  if (/^\d+$/.test(query)) {
    lookups.push({ kind: 'pr', request: api.githubView(roomId, 'pr', Number(query)) })
    lookups.push({ kind: 'issue', request: api.githubView(roomId, 'issue', Number(query)) })
  }
  const sources = await Promise.allSettled(lookups.map(({ request }) => request))
  const references = new Map<string, Reference>()
  sources.forEach((source, index) => {
    if (source.status !== 'fulfilled') return
    const lookup = lookups[index]
    if (!lookup) return
    const found = lookup.kind === 'room_issue' ? issueRows(source.value, roomId) : rows(source.value, index < 3)
    for (const row of found) {
      const reference: Reference = { kind: lookup.kind, ...row }
      references.set(referenceKey(reference), reference)
    }
  })
  if (sources.every((source) => source.status === 'rejected')) throw new Error('Could not load PRs and issues. Try again.')
  return [...references.values()]
}
