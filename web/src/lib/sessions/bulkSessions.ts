import type { Member, ProfileChoice } from '../api/room.ts'
import type { BulkSessionItem, BulkSessionResult, TransferState } from '../api/session.ts'
import { errorText } from '../errorText.ts'

export type BulkAction = 'stop' | 'close' | 'promote' | 'adopt' | 'transfer'
// The fields of a result that these helpers read.
export interface ResultLike {
  ok: boolean
  error?: string
  member?: { closeSummary?: string[] }
  transfer?: Pick<TransferState, 'phase'> & Partial<TransferState>
}
// A result row, with the note a failed progress check leaves on it.
export type BulkRow = BulkSessionItem & { progressError?: string }

export const BULK_CHUNK_SIZE = 512
export const bulkRetryable = (r: ResultLike, action: string): boolean => !r.ok || action === 'transfer' && ['failed', 'cancelled'].includes(r.transfer?.phase ?? '')
export const transferRunning = (r: ResultLike): boolean => r.ok && ['waiting', 'stopping', 'starting'].includes(r.transfer?.phase ?? '')
export const bulkTitles: Record<BulkAction, string> = { stop: 'Stop sessions', close: 'Close sessions', promote: 'Promote to lead', adopt: 'Move under…', transfer: 'Transfer sessions to a model' }
const TRANSFER_TEXT: Record<string, string> = { completed: 'New model ready', failed: 'Transfer needs attention', cancelled: 'Transfer cancelled', waiting: 'Transfer accepted · waiting for handoff', stopping: 'Transfer accepted · switching models', starting: 'Transfer accepted · starting model' }
const ACTION_TEXT: Record<string, string> = { stop: 'Stop accepted', close: 'Close accepted', promote: 'Promoted to lead', adopt: 'Moved' }
export function bulkResultText(r: ResultLike, action: string): string {
  if (!r.ok) return r.error || 'The host refused this action.'
  if (action === 'close' && r.member?.closeSummary?.length) return r.member.closeSummary.join('; ')
  if (action === 'transfer') return TRANSFER_TEXT[r.transfer?.phase ?? ''] ?? 'Transfer accepted'
  return ACTION_TEXT[action] ?? 'Accepted'
}

// adoptTargets lists the open sessions the selection can move under, as
// { member, reason }. reason is '' for a valid parent, or why the engine
// would refuse it: a cycle, or a depth past maxDepth (0 means no limit). The
// engine checks again; this only explains the choice. A service is no member,
// so it is never a target. members come from the Members API, whose display
// depth starts at 0 for a top-level session; stored session depth, which
// maxDepth limits, starts at 1.
type AdoptMember = Pick<Member, 'name' | 'depth'> & Partial<Pick<Member, 'id' | 'owner' | 'closed' | 'parent'>>
export function adoptTargets<T extends AdoptMember>(members: T[], ids: string[], maxDepth: number): { member: T; reason: string }[] {
  const open = members.filter((m) => m.id && !m.owner && !m.closed)
  const kids = new Map<string | undefined, T[]>()
  for (const m of open) kids.set(m.parent, [...(kids.get(m.parent) ?? []), m])
  const below = (m: T): T[] => (kids.get(m.id) ?? []).flatMap((k) => [k, ...below(k)])
  const moved = open.filter((m) => m.id !== undefined && ids.includes(m.id))
  const inside = new Set(moved.flatMap((m) => [m, ...below(m)]).map((m) => m.id))
  // Each selected session brings its subtree; its deepest member sets the limit.
  const deepest = moved.map((m) => ({ who: below(m).reduce((d, k) => (k.depth > d.depth ? k : d), m), from: m })).map(({ who, from }) => ({ who, extra: who.depth - from.depth }))
  return open.filter((t) => t.id === undefined || !ids.includes(t.id)).map((t) => {
    if (inside.has(t.id)) return { member: t, reason: 'Inside the selection' }
    const over = deepest.map(({ who, extra }) => ({ who, at: t.depth + 2 + extra })).find(({ at }) => maxDepth > 0 && at > maxDepth)
    return { member: t, reason: over ? `${over.who.name} would be at depth ${over.at} of ${maxDepth}` : '' }
  })
}

// The calls the controller makes.
export interface BulkApi {
  settings(): Promise<{ key: string; value?: unknown }[] | null>
  profiles(): Promise<ProfileChoice[] | null>
  bulkSessionAction(roomId: string, options: BulkOptions): Promise<BulkSessionResult>
  sessionTransfer(roomId: string, session: string): Promise<TransferState | null>
}
// What the request sends. Close hands work up the parent chain; force permits failed handoffs.
export interface BulkOptions {
  action: string
  profile?: string
  parent?: string
  sessions?: string[]
  force?: boolean
}
export interface BulkContext {
  roomId: string
  sessions: { id: string }[]
  action: BulkAction
}
// What the screen shows.
export interface BulkValue {
  action: BulkAction
  results: BulkRow[]
  profiles: ProfileChoice[]
  profile: string
  parent: string
  maxDepth: number
  loading: boolean
  busy: boolean
  started: boolean
  error: string
}

// App-owned, not dialog-owned: closing the screen does not stop chunk dispatch.
// Destroy only when the app unmounts. Already accepted host work stays running.
export function createBulkSessionAction(api: BulkApi, context: BulkContext, changed: (value: BulkValue) => void) {
  const { roomId, sessions, action } = context
  const ids = sessions.map((m) => m.id)
  let alive = true, generation = 0, poll = 0, polling = false
  let options: BulkOptions | null = null
  let value: BulkValue = { action, results: [], profiles: [], profile: '', parent: '', maxDepth: 0, loading: action === 'transfer' || action === 'adopt', busy: false, started: false, error: '' }
  const publish = (patch: Partial<BulkValue>) => { if (alive) { value = { ...value, ...patch }; changed(value) } }
  const current = (g: number) => alive && g === generation
  async function load() {
    if (action === 'adopt') {
      const g = ++generation
      try {
        const max = (await api.settings())?.find((it) => it.key === 'subagent.maxDepth')?.value
        if (current(g)) publish({ maxDepth: Number(max) || 0, loading: false, error: '' })
      } catch { if (current(g)) publish({ loading: false }) }
      return
    }
    if (action !== 'transfer') { publish({ loading: false }); return }
    const g = ++generation
    try {
      const profiles = await api.profiles()
      if (current(g)) publish({ profiles: profiles ?? [], loading: false, error: '' })
    } catch { if (current(g)) publish({ loading: false, error: 'Couldn’t load models. Try again.' }) }
  }
  function configure(patch: Partial<Pick<BulkValue, 'profile' | 'parent'>>) { if (!value.busy && !value.started) publish({ ...patch, error: '' }) }
  async function run(force = false) {
    if (!alive || value.busy || value.loading) return
    if (!options) {
      const chosen: BulkOptions = { action }
      if (action === 'transfer') {
        if (!value.profiles.some((p) => p.name === value.profile)) { publish({ error: 'Choose one model for the selected sessions.' }); return }
        chosen.profile = value.profile
      }
      if (action === 'adopt') {
        if (!value.parent) { publish({ error: 'Choose a parent session.' }); return }
        chosen.parent = value.parent
      }
      options = chosen
    }
    const pending = value.started ? value.results.filter((r) => bulkRetryable(r, action)).map((r) => r.session) : ids
    if (!pending.length) return
    const g = ++generation
    poll++; polling = false
    const results = new Map(value.results.map((r) => [r.session, r]))
    publish({ busy: true, started: true, error: '' })
    for (let offset = 0; offset < pending.length; offset += BULK_CHUNK_SIZE) {
      if (!current(g)) return
      const chunk = pending.slice(offset, offset + BULK_CHUNK_SIZE)
      try {
        const response = await api.bulkSessionAction(roomId, { ...options, sessions: chunk, ...(force ? { force: true } : {}) })
        if (!current(g)) return
        const seen = new Set<string>()
        const items = response?.results
        if (response?.action !== action || !Array.isArray(items) || items.length !== chunk.length || items.some((r) => !chunk.includes(r.session) || seen.has(r.session) || typeof r.ok !== 'boolean' || !seen.add(r.session))) throw new Error('The host returned an incomplete result. Check session state before retrying.')
        for (const r of items) results.set(r.session, r)
        publish({ results: ids.flatMap((id) => results.get(id) ?? []) })
      } catch (e) {
        if (!current(g)) return
        const error = errorText(e)
        for (const id of pending.slice(offset)) results.set(id, { session: id, ok: false, error: chunk.includes(id) ? 'Outcome not confirmed. Check this session before retrying.' : 'Not submitted after the preceding request failed.' })
        publish({ error, results: ids.flatMap((id) => results.get(id) ?? []) })
        break
      }
    }
    if (current(g)) publish({ busy: false })
  }
  async function refreshTransfers() {
    if (!alive || action !== 'transfer' || value.busy || polling) return
    const pending = value.results.filter(transferRunning)
    if (!pending.length) return
    const g = generation, serial = ++poll
    polling = true
    for (let offset = 0; offset < pending.length; offset += 8) {
      if (!current(g) || serial !== poll) return
      const next = await Promise.all(pending.slice(offset, offset + 8).map(async (r): Promise<BulkRow> => {
        try {
          const transfer = await api.sessionTransfer(roomId, r.session)
          if (!transfer || transfer.session !== r.session || r.transfer?.id && transfer.id !== r.transfer.id) throw new Error('Different transfer')
          return { ...r, transfer, progressError: '' }
        }
        catch { return { ...r, progressError: 'Couldn’t check transfer progress.' } }
      }))
      if (!current(g) || serial !== poll) return
      const byId = new Map(next.map((r) => [r.session, r]))
      publish({ results: value.results.map((r) => byId.get(r.session) ?? r) })
    }
    if (current(g) && serial === poll) polling = false
  }
  publish({})
  return { load, configure, run, refreshTransfers, destroy() { alive = false; generation++; poll++ } }
}
