import type { Member, ProfileChoice } from '../api/room.ts'
import type { ConsolidationState } from '../api/session.ts'
import { errorText } from '../errorText.ts'

// A selection names sessions, never list positions: hierarchy and dormant order
// can change while the member list is open.
export const MAX_CONSOLIDATION_SESSIONS = 16
// The fields of a member that the selection reads.
type Selectable = Partial<Pick<Member, 'id' | 'owner' | 'closed' | 'role'>>
export const activeConsolidation = (op?: Pick<ConsolidationState, 'phase'> & Partial<ConsolidationState> | null): boolean => !!op?.cancelPending || ['waiting', 'stopping', 'starting', 'committing'].includes(op?.phase ?? '')
// sessionIds lists the IDs a selection holds; a member without one is not selectable.
export const sessionIds = (members: readonly Selectable[]): string[] => members.flatMap((m) => (m.id ? [m.id] : []))
export const selectableSession = (m?: Selectable | null): boolean => !!(m?.id && !m.owner && !m.closed && m.role !== 'root')
export const selectionKey = (roomId: string, ids: string[], operationId = ''): string => JSON.stringify([roomId, [...new Set(ids)].sort(), operationId])
export function pruneSelection(ids: string[], members: Selectable[]): string[] {
  const eligible = new Set(members.filter(selectableSession).map((m) => m.id))
  return [...new Set(ids)].filter((id) => eligible.has(id))
}
export function toggleSelection(ids: string[], member: Selectable & { id: string }, checked: boolean): string[] {
  if (!checked) return ids.filter((id) => id !== member.id)
  if (!selectableSession(member) || ids.includes(member.id)) return ids
  return [...ids, member.id]
}
const PHASE_TEXT: Record<string, string> = {
  waiting: 'Collecting handoffs…', stopping: 'Preparing the sessions…',
  starting: 'Starting the kept agent…', committing: 'Finishing consolidation…',
  completed: 'Consolidation complete.', failed: 'Consolidation needs attention.', cancelled: 'Consolidation cancelled.',
}
export const consolidationPhase = (op?: Partial<ConsolidationState> | null): string => op?.cancelPending ? 'Finishing cancellation…' : PHASE_TEXT[op?.phase ?? ''] ?? ''

// The calls the controller makes.
export interface ConsolidationApi {
  profiles(): Promise<ProfileChoice[] | null>
  sessionConsolidations(roomId: string): Promise<ConsolidationState[] | null>
  consolidateSessions(roomId: string, options: ConsolidateOptions): Promise<ConsolidationState>
  resumeSessionConsolidation(roomId: string, id: string): Promise<ConsolidationState>
  cancelSessionConsolidation(roomId: string, id: string): Promise<ConsolidationState>
}
export interface ConsolidateOptions {
  sessions: string[]
  target: string
  profile?: string
}
export interface ConsolidationContext {
  roomId: string
  sessions: Selectable[]
  operationId?: string
}
// What the screen shows.
export interface ConsolidationValue {
  operation: ConsolidationState | null
  target: string
  profile: string
  profiles: ProfileChoice[]
  loading: boolean
  busy: boolean
  error: string
  modelError: string
}

// Each screen owns one immutable room/selection/operation context. All requests
// carry a revision, and polling also carries a serial. Closing destroys only
// this view, never the durable host operation.
export function createConsolidationView(api: ConsolidationApi, context: ConsolidationContext, changed: (value: ConsolidationValue) => void) {
  const { roomId, sessions, operationId = '' } = context
  const ids = sessionIds(sessions)
  const fingerprint = selectionKey(roomId, ids)
  let alive = true, revision = 0, serial = 0, polling = false
  let value: ConsolidationValue = { operation: null, target: ids[0] ?? '', profile: '', profiles: [], loading: true, busy: false, error: '', modelError: '' }
  const publish = (patch: Partial<ConsolidationValue>) => { if (alive) { value = { ...value, ...patch }; changed(value) } }
  const current = (r: number, s: number) => alive && r === revision && s === serial
  const match = (list: ConsolidationState[]) => operationId
    ? list.find((op) => op.id === operationId)
    : list.find((op) => selectionKey(roomId, op.sessions ?? []) === fingerprint && !['completed', 'cancelled'].includes(op.phase))
  const valid = (op: ConsolidationState | undefined, expected = ''): op is ConsolidationState => !!op?.id && op.roomId === roomId && (!expected || op.id === expected) &&
    (!!operationId || !!expected || selectionKey(roomId, op.sessions ?? []) === fingerprint && ids.includes(op.target))
  const accept = (op: ConsolidationState) => publish({ operation: op, target: op.target, profile: '' })

  async function load() {
    const r = ++revision, s = ++serial
    publish({ loading: true, error: '' })
    const [operations, profiles] = await Promise.allSettled([Promise.resolve().then(() => api.sessionConsolidations(roomId)), Promise.resolve().then(() => api.profiles())])
    if (!current(r, s)) return
    publish({ profiles: profiles.status === 'fulfilled' ? (profiles.value ?? []) : [], modelError: profiles.status === 'rejected' ? 'Couldn’t load other models. The current model is still available.' : '' })
    if (operations.status === 'rejected') { publish({ error: 'Couldn’t check saved consolidations. Try again.', loading: false }); return }
    const op = match(operations.value ?? [])
    if (op && valid(op, operationId)) accept(op)
    else if (operationId) { publish({ error: 'This saved consolidation is unavailable. Try again.', loading: false }); return }
    publish({ loading: false })
    if (activeConsolidation(op)) await resume()
  }
  async function refresh() {
    if (!alive || value.busy || polling || !value.operation) return
    const r = revision, s = ++serial, expected = value.operation.id
    polling = true
    try {
      const operations = await api.sessionConsolidations(roomId)
      if (!current(r, s)) return
      const op = (operations ?? []).find((op) => op.id === expected)
      if (!valid(op, expected)) { publish({ error: 'Couldn’t find the saved consolidation. Check progress again.' }); return }
      accept(op)
      publish({ error: '' })
    } catch (e) { if (current(r, s)) publish({ error: 'Couldn’t check consolidation progress. Try again.' }) }
    finally { if (current(r, s)) polling = false }
  }
  async function mutate(method: () => Promise<ConsolidationState>, expected = '', expectedTarget = '') {
    if (!alive || value.busy) return
    const r = ++revision, s = ++serial
    polling = false
    publish({ busy: true, error: '' })
    try {
      const op = await method()
      if (!current(r, s)) return
      if (!valid(op, expected) || expectedTarget && op.target !== expectedTarget) throw new Error('The host returned a different consolidation. Check progress again.')
      accept(op)
    } catch (e) {
      if (!current(r, s)) return
      publish({ error: errorText(e) })
      // A launch can fail after the host saved the operation. Rediscover it
      // before offering another start; never make a second operation on retry.
      try {
        const operations = await api.sessionConsolidations(roomId)
        if (!current(r, s)) return
        const op = expected ? (operations ?? []).find((op) => op.id === expected) : match(operations ?? [])
        if (valid(op, expected)) accept(op)
      } catch { /* Keep the mutation error and offer a progress check. */ }
    } finally { if (current(r, s)) publish({ busy: false }) }
  }
  async function start() {
    if (!alive || value.operation || value.loading || value.busy || value.error) return
    if (ids.length < 2 || ids.length > MAX_CONSOLIDATION_SESSIONS || new Set(ids).size !== ids.length || !ids.includes(value.target) || !sessions.every(selectableSession)) {
      publish({ error: 'Choose 2–16 open sessions and keep one of them.' }); return
    }
    const options: ConsolidateOptions = { sessions: [...ids], target: value.target }
    if (value.profile) {
      const p = value.profiles.find((p) => p.name === value.profile)
      if (!p) { publish({ error: 'That model is no longer available. Choose another model.' }); return }
      options.profile = p.name
    }
    await mutate(() => api.consolidateSessions(roomId, options), '', options.target)
  }
  async function resume() {
    const op = value.operation
    if (!op || !activeConsolidation(op) && op.phase !== 'failed') return
    await mutate(() => api.resumeSessionConsolidation(roomId, op.id), op.id)
  }
  async function cancel() {
    const op = value.operation
    if (!op || !op.cancelPending && !['waiting', 'failed'].includes(op.phase)) return
    await mutate(() => api.cancelSessionConsolidation(roomId, op.id), op.id)
  }
  return {
    load, refresh, start, resume, cancel,
    chooseTarget(id: string) { if (!value.operation && !value.busy && ids.includes(id)) publish({ target: id, profile: '' }) },
    chooseProfile(name: string) { if (!value.operation && !value.busy && (!name || value.profiles.some((p) => p.name === name))) publish({ profile: name }) },
    destroy() { alive = false; revision++; serial++; polling = false },
  }
}
