import { createReloadGate } from '../ui/reloadGate.ts'

export const roomControlLabels: Record<string, string> = { pause: 'Pause all', resume: 'Resume all', clearDormant: 'Clear dormant sessions' }
const statusLabels: Record<string, string> = { paused: 'Paused', running: 'Running turn continues', held: 'Held for resume', idle: 'Idle', resumed: 'Resumed', closed: 'Closed', kept: 'Kept open', error: 'The action failed.' }

// KeptReason names why the engine kept a session open. The host sends a named
// reason, so the web never matches an error message.
export type KeptReason = 'started-work' | 'responsibilities' | 'failed'
const keptReasons: KeptReason[] = ['started-work', 'responsibilities', 'failed']

// How a room's sessions run now. The engine sends more fields; these are the
// ones the controls read.
export interface RoomControlStatus {
  roomId: string
  paused: boolean
  running: number
  held: number
  dormant: number
}

// One session's answer to a control action.
export interface RoomControlResult {
  session?: string
  name?: string
  ok: boolean
  status: string
  reason?: KeptReason
  error?: string
}

// What the host answers a control action with. It is validated before it is
// shown, so it arrives untyped.
export interface RoomControlResponse {
  state: RoomControlStatus
  results: RoomControlResult[]
}

// The host calls behind the controls.
export interface RoomControlsApi {
  roomSessionControls(roomId: string): Promise<unknown>
  pauseRoomSessions(roomId: string): Promise<unknown>
  resumeRoomSessions(roomId: string): Promise<unknown>
  clearDormantSessions(roomId: string): Promise<unknown>
}

// What the controls show: the room's state, the running action and the
// per-session results.
export interface RoomControlValue {
  state: RoomControlStatus | null
  loading: boolean
  busy: boolean
  action: string
  results: RoomControlResult[]
  error: string
  statusError: string
}

export function roomControlResult(result: RoomControlResult, action: string): string {
  if (result.reason === 'started-work') return 'Kept open · it gained new work'
  if (result.reason === 'responsibilities') return 'Kept open · it still has open work'
  if (!result.ok) return result.error || statusLabels[result.status] || 'The host refused this action.'
  return statusLabels[result.status] || ({ pause: 'Paused', resume: 'Resumed', clearDormant: 'Closed' }[action] ?? '')
}

// A session shown by name in a result. The members list is a fallback only.
export interface SessionName { id?: string; address?: string }

// One line of the Details view: the session and what the action did to it.
export interface RoomControlDetail { name: string; text: string; error: boolean }

// What a finished action reports as a short toast with optional details.
export interface RoomControlSummary { title: string; text: string; details: RoomControlDetail[] }

// sessionName shows a session by name. It never shows a raw session ID.
export function sessionName(result: RoomControlResult, members: SessionName[] = []): string {
  const known = result.name || members.find((member) => member.id && member.id === result.session)?.address
  if (known) return known
  const tail = (result.session ?? '').replace(/^ses_/, '').split('-').pop()
  return tail ? `session ${tail}` : 'a session'
}

const count = (n: number, one: string): string => `${n} ${n === 1 ? one : `${one}s`}`

// roomControlSummary is the one place an action's results become words. A
// session kept for work it has gained or still holds is a neutral outcome;
// only a real failure is an error.
export function roomControlSummary(value: RoomControlValue, members: SessionName[] = []): RoomControlSummary {
  const results = value.results
  const closed = results.filter((r) => r.ok && r.status === 'closed').length
  const keptWork = results.filter((r) => r.ok && r.reason === 'started-work').length
  const keptOpen = results.filter((r) => r.ok && r.reason === 'responsibilities').length
  const failed = results.filter((r) => !r.ok).length
  const parts: string[] = []
  if (value.action === 'clearDormant') {
    parts.push(closed ? `Closed ${count(closed, 'dormant session')}.` : 'No dormant sessions were closed.')
    if (keptWork) parts.push(`${keptWork} kept because ${keptWork === 1 ? 'it had' : 'they had'} new work.`)
    if (keptOpen) parts.push(`${keptOpen} kept because ${keptOpen === 1 ? 'it still has' : 'they still have'} open work.`)
    if (failed) parts.push(`${count(failed, 'session')} could not be closed.`)
  } else {
    const done = results.filter((r) => r.ok).length
    const verb = value.action === 'resume' ? 'Resumed' : 'Paused'
    parts.push(done ? `${verb} ${count(done, 'session')}.` : 'No sessions needed a change.')
    if (failed) parts.push(`${count(failed, 'session')} could not be ${value.action === 'resume' ? 'resumed' : 'paused'}.`)
  }
  return {
    title: roomControlLabels[value.action] ?? 'Session action',
    text: parts.join(' '),
    details: results.map((result) => ({ name: sessionName(result, members), text: roomControlResult(result, value.action), error: !result.ok })),
  }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null
}

const isCount = (value: unknown): boolean => typeof value === 'number' && Number.isSafeInteger(value) && value >= 0

// The host error as text: its message when it has one, else the thrown value.
const errorMessage = (e: unknown): string => String((isRecord(e) ? e.message : '') || e)

// One controller belongs to one room visit. Destroying the view never cancels
// an accepted host mutation, but late callbacks cannot update a later visit.
export function createRoomSessionControls(api: RoomControlsApi, roomId: string, changed: (value: RoomControlValue) => void, visible = () => true) {
  let alive = true, revision = 0
  let value: RoomControlValue = { state: null, loading: false, busy: false, action: '', results: [], error: '', statusError: '' }
  const publish = (patch: Partial<RoomControlValue>) => { if (alive) { value = { ...value, ...patch }; changed(value) } }
  const current = (r: number) => alive && r === revision
  const validState = (state: unknown): state is RoomControlStatus =>
    isRecord(state) && state.roomId === roomId && typeof state.paused === 'boolean' &&
    ['running', 'held', 'dormant'].every((key) => isCount(state[key]))
  const validResult = (result: unknown): result is RoomControlResult =>
    isRecord(result) && typeof result.session === 'string' && result.session !== '' && typeof result.ok === 'boolean' &&
    (result.name === undefined || typeof result.name === 'string') &&
    (result.reason === undefined || keptReasons.includes(result.reason as KeptReason))
  const reload = createReloadGate(() => ({ room: roomId, visit: revision }), () => alive && !value.busy && visible())
  function load() { return reload('roomControls', async (valid) => {
    const r = revision
    publish({ loading: true })
    try {
      const state = await api.roomSessionControls(roomId)
      if (!valid() || !current(r)) return
      if (!validState(state)) throw new Error('Couldn’t read valid session controls for this room.')
      publish({ state, statusError: '' })
    } catch (e) { if (valid() && current(r)) publish({ statusError: errorMessage(e) }) }
    finally { if (alive) publish({ loading: false }) }
  }) }
  async function run(action: string) {
    const calls: Record<string, ((roomId: string) => Promise<unknown>) | undefined> = { pause: api.pauseRoomSessions, resume: api.resumeRoomSessions, clearDormant: api.clearDormantSessions }
    const call = calls[action]
    if (!alive || value.busy || !roomControlLabels[action] || !call) return
    const r = ++revision
    publish({ action, busy: true, results: [], error: '' })
    try {
      const response = await call(roomId)
      if (!current(r)) return
      if (!isRecord(response) || !validState(response.state) || !Array.isArray(response.results) || response.results.some((result) => !validResult(result))) throw new Error('The host returned an incomplete result. Refresh room status before retrying.')
      publish({ state: response.state, results: response.results, statusError: '' })
    } catch (e) { if (current(r)) publish({ error: errorMessage(e) }) }
    finally { if (current(r)) publish({ busy: false }) }
  }
  publish({})
  return { load, run, destroy() { alive = false; revision++ } }
}
