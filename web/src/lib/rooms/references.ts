import { api } from '../../api.js'

export interface Reference {
  kind: 'pr' | 'issue' | 'room_issue'
  number: number | string
  title: string
  state: string
  url: string
}

export const referenceKey = (reference: Reference) => `${reference.kind}:${reference.number}`
export const referenceLabel = (reference: Reference) => `${reference.kind === 'pr' ? 'PR' : reference.kind === 'room_issue' ? 'Room issue' : 'Issue'} #${reference.number}`

export function matchReferences(references: Reference[], query: string) {
  const search = query.replace(/^#/, '').trim().toLowerCase()
  return references.filter((reference) => `${reference.number} ${reference.title}`.toLowerCase().includes(search))
    .sort((a, b) => Number(b.state.toLowerCase() === 'open') - Number(a.state.toLowerCase() === 'open'))
    .slice(0, 12)
}

export async function loadReferences(roomId: string, query = ''): Promise<Reference[]> {
  const state = query ? 'all' : 'open'
  const search = query && !/^\d+$/.test(query) ? `${query} in:title` : query
  const requests = [
    { kind: 'pr', request: api.githubList(roomId, 'pr', state, search) },
    { kind: 'issue', request: api.githubList(roomId, 'issue', state, search) },
    { kind: 'room_issue', request: api.issues(roomId, true) },
  ]
  // GitHub text search does not reliably find a row by its number.
  if (/^\d+$/.test(query)) {
    requests.push({ kind: 'pr', request: api.githubView(roomId, 'pr', Number(query)) })
    requests.push({ kind: 'issue', request: api.githubView(roomId, 'issue', Number(query)) })
  }
  const sources = await Promise.allSettled(requests.map(({request}) => request))
  const references = new Map<string, Reference>()
  sources.forEach((source, index) => {
    if (source.status !== 'fulfilled') return
    const request = requests[index]
    if (!request) return
    const kind = request.kind as Reference['kind']
    const items = kind === 'room_issue' ? source.value : index < 3 ? source.value?.items : [source.value]
    for (const item of items ?? []) {
      const reference: Reference = kind === 'room_issue'
        ? { kind, number: item.id, title: item.title, state: item.state, url: `#room=${encodeURIComponent(roomId)}&view=issues` }
        : { kind, number: item.number, title: item.title, state: item.state, url: item.url }
      references.set(referenceKey(reference), reference)
    }
  })
  if (sources.every((source) => source.status === 'rejected')) throw new Error('Could not load PRs and issues. Try again.')
  return [...references.values()]
}
