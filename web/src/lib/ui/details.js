import { roomIDPattern } from './posts.js'

// Local UI targets carry store IDs, never commands to execute.
export const detailIDs = {
  shell: /^sh_\d{8}-\d{6}-[a-f0-9]{8}$/,
  run: /^wf_\d{8}-\d{6}-[a-f0-9]{8}$/,
  session: /^ses_\d{8}-\d{6}-[a-f0-9]{8}$/,
}

export const detailLabels = { shell: 'View log', run: 'Open workflow', session: 'View transcript' }

export function detailHref(detail, room = '') {
  if (!detailIDs[detail?.kind]?.test(detail.id)) return ''
  const q = new URLSearchParams({ 'dyna-detail': detail.kind, id: detail.id })
  if (detail.kind === 'session' && Number.isSafeInteger(detail.turn) && detail.turn > 0) q.set('turn', detail.turn)
  if (roomIDPattern.test(room)) q.set('room', room)
  return `#${q}`
}

export function parseDetailHref(href) {
  if (!href?.startsWith('#dyna-detail=')) return null
  const q = new URLSearchParams(href.slice(1))
  const kind = q.get('dyna-detail')
  const id = q.get('id')
  if (!detailIDs[kind]?.test(id || '')) return null
  const turn = q.has('turn') ? Number(q.get('turn')) : 0
  if (q.has('turn') && (kind !== 'session' || !Number.isSafeInteger(turn) || turn < 1)) return null
  if (q.has('room') && !roomIDPattern.test(q.get('room'))) return null
  return { kind, id, turn, ...(q.has('room') ? { roomId: q.get('room') } : {}) }
}
