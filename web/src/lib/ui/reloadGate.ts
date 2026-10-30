import type { Member } from '../api/room.ts'
import { isFeedView } from '../rooms/feedViews.ts'

type Selection = { room: unknown; visit: unknown }
type Load = (current: () => boolean) => void | Promise<unknown>
type Task<S> = { value: S; load: Load }
type Entry<S> = { next: Task<S> | null; patches: Task<S>[]; promise: Promise<void> }

// Each room/list has one request and one pending refresh. Every caller waits
// for the whole burst, including its trailing refresh.
export function createReloadGate<S extends Selection>(selection: () => S, visible: (name: string) => boolean = () => true) {
  const active = new Map<string, Entry<S>>()
  function context(name: string) {
    const value = { ...selection() }
    return value.room && visible(name) ? { value, key: name + ':' + value.room } : null
  }
  function reload(name: string, load: Load) {
    const selected = context(name)
    if (!selected) return Promise.resolve()
    const { value, key } = selected
    const existing = active.get(key)
    if (existing) {
      existing.next = { value, load }
      return existing.promise
    }
    const entry: Entry<S> = { next: null, patches: [], promise: Promise.resolve() }
    active.set(key, entry)
    const current = (v: S) => {
      const now = selection()
      return v.room === now.room && v.visit === now.visit && visible(name)
    }
    entry.promise = (async () => {
      let task: Task<S> | undefined = { value, load }
      try {
        do {
          entry.next = null
          const running = task
          if (current(running.value)) await running.load(() => current(running.value))
          task = entry.next ?? entry.patches.shift()
        } while (task)
      } finally { active.delete(key) }
    })()
    return entry.promise
  }
  // History loads and item patches must survive a trailing refresh. Keep
  // them in order after the request burst, under their original visits.
  reload.patch = (name: string, load: Load) => {
    const selected = context(name)
    if (!selected) return Promise.resolve()
    const entry = active.get(selected.key)
    if (!entry) return reload(name, load)
    entry.patches.push({ value: selected.value, load })
    return entry.promise
  }
  return reload
}

type Visibility = {
  view: string
  reviewOpen?: boolean
  drill?: Partial<Member> | null
  showRoomSettings?: boolean
  memberChips?: boolean
  sidebarChips?: boolean
  sideVisible?: boolean
  controlsOpen?: boolean
}

export function listVisible(name: string, { view, drill, showRoomSettings, memberChips, sidebarChips, sideVisible, controlsOpen, reviewOpen }: Visibility) {
  if (name === 'roomControls') return !!memberChips || !!showRoomSettings || !!controlsOpen
  if (name === 'computers') return !!memberChips || !!sideVisible || !!drill || view === 'computers'
  if (name === 'mentions') return !drill && isFeedView(view)
  // Changes rows feed the PR screen, the review workspace and the PR chips.
  if (name === 'changes' || name === 'reviewer') return !!reviewOpen || (!drill && view === 'github') || !!drill || !!showRoomSettings || !!memberChips || !!sidebarChips
  // Goal priority also feeds PR controls and the agent and PR badges.
  if (name === 'goals') return !!memberChips || !!sidebarChips || !!drill || view === 'github' || view === 'goals' || isFeedView(view)
  // Cards in chat, a session's media and the Visuals view all show the
  // newest version of their visual.
  if (name === 'visuals') return !!drill || view === 'visuals' || isFeedView(view)
  if (name === 'rules') return !drill && view === 'rules'
  // Members, questions, pins, computers and job counts also feed the chrome.
  return true
}

// Keep keyed row objects so Svelte only updates fields that changed.
export function reconcileRows<T extends object>(previous: T[], incoming: T[], key: string | ((row: T) => unknown) = 'id'): T[] {
  const identity = typeof key === 'function' ? key : (row: T) => (row as Record<string, unknown>)[key]
  const byId = new Map(previous.map((row) => [identity(row), row]))
  const rows = incoming.map((row) => {
    const old = byId.get(identity(row))
    if (!old || old === row) return row
    const oldFields = old as Record<string, unknown>
    const fields = row as Record<string, unknown>
    for (const field of Object.keys(old)) if (!(field in row)) delete oldFields[field]
    for (const field of Object.keys(row)) {
      if (JSON.stringify(oldFields[field]) !== JSON.stringify(fields[field])) oldFields[field] = fields[field]
    }
    return old
  })
  return rows.length === previous.length && rows.every((row, i) => row === previous[i]) ? previous : rows
}
