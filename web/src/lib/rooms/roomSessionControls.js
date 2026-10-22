import { createReloadGate } from '../ui/reloadGate.js'

export const roomControlLabels = { pause: 'Pause all', resume: 'Resume all', clearDormant: 'Clear dormant sessions' }
const statusLabels = { paused: 'Paused', running: 'Running turn continues', held: 'Held for resume', idle: 'Idle', resumed: 'Resumed', closed: 'Closed', kept: 'Kept open', error: 'The action failed.' }
export function roomControlResult(result, action) {
  if (!result.ok) return result.error || statusLabels[result.status] || 'The host refused this action.'
  return statusLabels[result.status] || ({ pause: 'Paused', resume: 'Resumed', clearDormant: 'Closed' }[action])
}

// One controller belongs to one room visit. Destroying the view never cancels
// an accepted host mutation, but late callbacks cannot update a later visit.
export function createRoomSessionControls(api, roomId, changed, visible = () => true) {
  let alive = true, revision = 0
  let value = { state: null, loading: false, busy: false, action: '', results: [], error: '', statusError: '' }
  const publish = (patch) => { if (alive) { value = { ...value, ...patch }; changed(value) } }
  const current = (r) => alive && r === revision
  const validState = (state) => state?.roomId === roomId && typeof state.paused === 'boolean' &&
    ['running', 'held', 'dormant'].every((key) => Number.isSafeInteger(state[key]) && state[key] >= 0)
  const reload = createReloadGate(() => ({ room: roomId, visit: revision }), () => alive && !value.busy && visible())
  function load() { return reload('roomControls', async (valid) => {
    const r = revision
    publish({ loading: true })
    try {
      const state = await api.roomSessionControls(roomId)
      if (!valid() || !current(r)) return
      if (!validState(state)) throw new Error('Couldn’t read valid session controls for this room.')
      publish({ state, statusError: '' })
    } catch (e) { if (valid() && current(r)) publish({ statusError: String(e?.message || e) }) }
    finally { if (alive) publish({ loading: false }) }
  }) }
  async function run(action) {
    if (!alive || value.busy || !roomControlLabels[action]) return
    const r = ++revision
    publish({ action, busy: true, results: [], error: '' })
    try {
      const response = await ({ pause: api.pauseRoomSessions, resume: api.resumeRoomSessions, clearDormant: api.clearDormantSessions }[action])(roomId)
      if (!current(r)) return
      if (!validState(response?.state) || !Array.isArray(response.results) || response.results.some((result) => !result.session || typeof result.ok !== 'boolean')) throw new Error('The host returned an incomplete result. Refresh room status before retrying.')
      publish({ state: response.state, results: response.results, statusError: '' })
    } catch (e) { if (current(r)) publish({ error: String(e?.message || e) }) }
    finally { if (current(r)) publish({ busy: false }) }
  }
  publish({})
  return { load, run, destroy() { alive = false; revision++ } }
}
