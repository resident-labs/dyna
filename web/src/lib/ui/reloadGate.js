import { isChannel } from '../rooms/channels.ts'

// Each room/list has one request and one pending refresh. Every caller waits
// for the whole burst, including its trailing refresh.
export function createReloadGate(selection, visible = () => true) {
  const active = new Map()
  function context(name) {
    const value = { ...selection() }
    return value.room && visible(name) ? { value, key: name + ':' + value.room } : null
  }
  function reload(name, load) {
    const selected = context(name)
    if (!selected) return Promise.resolve()
    const { value, key } = selected
    const existing = active.get(key)
    if (existing) {
      existing.next = { value, load }
      return existing.promise
    }
    const entry = { next: null, patches: [], promise: null }
    active.set(key, entry)
    const current = (v) => {
      const now = selection()
      return v.room === now.room && v.visit === now.visit && visible(name)
    }
    entry.promise = (async () => {
      let task = { value, load }
      try {
        do {
          entry.next = null
          if (current(task.value)) await task.load(() => current(task.value))
          task = entry.next ?? entry.patches.shift()
        } while (task)
      } finally { active.delete(key) }
    })()
    return entry.promise
  }
  // History loads and item patches must survive a trailing refresh. Keep
  // them in order after the request burst, under their original visits.
  reload.patch = (name, load) => {
    const selected = context(name)
    if (!selected) return Promise.resolve()
    const entry = active.get(selected.key)
    if (!entry) return reload(name, load)
    entry.patches.push({ value: selected.value, load })
    return entry.promise
  }
  return reload
}

export function listVisible(name, { view, drill, showRoomSettings, memberChips, sidebarChips, sideVisible, controlsOpen }) {
  if (name === 'roomControls') return !!memberChips || !!showRoomSettings || !!controlsOpen
  if (name === 'computers') return !!memberChips || !!sideVisible || !!drill || view === 'computers'
  if (name === 'mentions') return !drill && isChannel(view)
  // Changes rows feed the PR screen, a session's PR tab and the PR chips.
  if (name === 'changes' || name === 'reviewer') return (!drill && view === 'github') || !!drill || !!showRoomSettings || !!memberChips || !!sidebarChips
  // Goal priority also feeds PR controls and the agent and PR badges.
  if (name === 'goals') return !!memberChips || !!sidebarChips || !!drill || view === 'github' || view === 'goals' || isChannel(view)
  // Cards in chat, a session's media and the Visuals view all show the
  // newest version of their visual.
  if (name === 'visuals') return !!drill || view === 'visuals' || isChannel(view)
  if (name === 'rules') return !!showRoomSettings
  // Members, questions, pins, computers and job counts also feed the chrome.
  return true
}

// Keep keyed row objects so Svelte only updates fields that changed.
export function reconcileRows(previous, incoming, key = 'id') {
  const identity = typeof key === 'function' ? key : (row) => row[key]
  const byId = new Map(previous.map((row) => [identity(row), row]))
  const rows = incoming.map((row) => {
    const old = byId.get(identity(row))
    if (!old || old === row) return row
    for (const field of Object.keys(old)) if (!(field in row)) delete old[field]
    for (const field of Object.keys(row)) {
      if (JSON.stringify(old[field]) !== JSON.stringify(row[field])) old[field] = row[field]
    }
    return old
  })
  return rows.length === previous.length && rows.every((row, i) => row === previous[i]) ? previous : rows
}
