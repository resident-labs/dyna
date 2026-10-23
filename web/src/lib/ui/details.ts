import { roomIDPattern } from './posts.ts'

// Local UI targets carry store IDs, never commands to execute.
export const detailIDs = {
  shell: /^sh_\d{8}-\d{6}-[a-f0-9]{8}$/,
  run: /^wf_\d{8}-\d{6}-[a-f0-9]{8}$/,
  session: /^ses_\d{8}-\d{6}-[a-f0-9]{8}$/,
}

export const detailLabels = { shell: 'View log', run: 'Open workflow', session: 'View transcript' }

export type DetailKind = keyof typeof detailIDs
const isKind = (kind: unknown): kind is DetailKind => typeof kind === 'string' && Object.hasOwn(detailIDs, kind)

export function detailHref(detail: unknown, room = '') {
  if (!detail || typeof detail !== 'object' || !('kind' in detail) || !isKind(detail.kind)
    || !('id' in detail) || typeof detail.id !== 'string' || !detailIDs[detail.kind].test(detail.id)) return ''
  const q = new URLSearchParams({ 'dyna-detail': detail.kind, id: detail.id })
  if (detail.kind === 'session' && 'turn' in detail && typeof detail.turn === 'number' && Number.isSafeInteger(detail.turn) && detail.turn > 0) q.set('turn', String(detail.turn))
  if (roomIDPattern.test(room)) q.set('room', room)
  return `#${q}`
}

export function parseDetailHref(href: string | null | undefined) {
  if (!href?.startsWith('#dyna-detail=')) return null
  const q = new URLSearchParams(href.slice(1))
  const kind = q.get('dyna-detail')
  const id = q.get('id')
  if (!isKind(kind) || !id || !detailIDs[kind].test(id)) return null
  const turn = q.has('turn') ? Number(q.get('turn')) : 0
  if (q.has('turn') && (kind !== 'session' || !Number.isSafeInteger(turn) || turn < 1)) return null
  const roomId = q.get('room')
  if (roomId !== null && !roomIDPattern.test(roomId)) return null
  return { kind, id, turn, ...(roomId !== null ? { roomId } : {}) }
}
