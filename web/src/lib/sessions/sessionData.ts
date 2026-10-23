import type { QueueItem, Session } from '../api/session.ts'
import { errorText } from '../errorText.ts'
import { createReloadGate } from '../ui/reloadGate.ts'

const TURNS_PER_PAGE = 6

// The calls the controller makes.
export interface SessionApi {
  sessionQueue(id: string): Promise<QueueItem[] | null>
  rootSession(room: string): Promise<Session>
  transcriptTurn(id: string, turn: number): Promise<Session>
  transcriptPage(id: string, cursor: string, turns: number): Promise<Session>
  transcriptAfter(id: string, cursor: string, turns: number): Promise<Session>
}

// The open session screen: which room, session, selection and turn it shows.
export interface SessionSelection {
  room: string
  visit: number
  id: string
  selection: string
  turn: number
  root: boolean
  live: boolean
  visible?: boolean
}

// How a transcript page joins the shown window: true is older, 'newer' reads
// forward, 'present' restores the newest page.
export type PageDirection = boolean | string

// What the controller hands back to the app.
export interface SessionHooks {
  current(): SessionSelection
  queue(queue: QueueItem[]): void
  transcript(data: Session, direction?: PageDirection): void
  error(message: string): void
}

// The page read in flight, so a repeat of it shares the answer.
interface HistoryEntry {
  value: SessionSelection
  direction: string
  promise: Promise<boolean>
}

export function createSessionData(api: SessionApi, hooks: SessionHooks) {
  let alive = true, request = 0, polling = false, timer: ReturnType<typeof setInterval> | undefined, postTimer: ReturnType<typeof setTimeout> | undefined
  let history: HistoryEntry | undefined
  const selected = (value: SessionSelection) => {
    const now = hooks.current()
    return alive && value.room === now.room && value.visit === now.visit && value.id === now.id && value.selection === now.selection && value.turn === now.turn
  }
  const reload = createReloadGate(() => {
    const value = hooks.current()
    return { room: value.id, visit: JSON.stringify([value.room, value.visit, value.selection, value.turn]) }
  }, () => alive && hooks.current().visible !== false)
  function load() { return reload('transcript', async (valid: () => boolean) => {
    const value = hooks.current(), revision = ++request
    if (!alive || !value.id) return
    const current = () => valid() && selected(value) && revision === request
    // The root conversation is one answer: it has no queue and no turn pages.
    const queued = value.root ? Promise.resolve() : api.sessionQueue(value.id).then((queue) => { if (current()) hooks.queue(queue ?? []) }, () => {})
    try {
      const data = value.root ? await api.rootSession(value.room) : value.turn ? await api.transcriptTurn(value.id, value.turn) : await api.transcriptPage(value.id, '', TURNS_PER_PAGE)
      if (current()) hooks.transcript(data)
    } catch (e) {
      if (!current()) return
      hooks.error(String(e))
    } finally { await queued }
  }) }
  function page(cursor: string, direction: string, near: () => boolean = () => true): Promise<boolean> {
    const value = hooks.current()
    // The root conversation has no turn pages; newer and present reload it whole.
    if (value.root && direction === 'older') return Promise.resolve(false)
    if (history && selected(history.value)) {
      if (history.direction === direction) return history.promise
      return history.promise.then(() => selected(value) && page(cursor, direction, near))
    }
    let loaded = false
    const entry: HistoryEntry = { value, direction, promise: Promise.resolve(false) }
    history = entry
    entry.promise = reload.patch('transcript', async (valid: () => boolean) => {
      if (!valid() || !selected(value)) return
      // Include the retained final turn: new conversation posts can arrive
      // without a new turn number. Its page also restores those posts.
      const after = Number(cursor) - 1
      const data = value.root ? await api.rootSession(value.room) : direction === 'newer' && cursor
        ? await api.transcriptAfter(value.id, after > 0 ? String(after) : '', TURNS_PER_PAGE)
        : await api.transcriptPage(value.id, cursor, TURNS_PER_PAGE)
      if (valid() && selected(value)) { if (near()) hooks.transcript(data, direction === 'older' ? true : value.root ? 'present' : direction); loaded = true }
    }).then(() => loaded).finally(() => { if (history === entry) history = undefined })
    return entry.promise
  }
  return {
    load,
    earlier: (cursor: string, near?: () => boolean) => page(cursor, 'older', near),
    newer: (cursor: string, near?: () => boolean) => page(cursor, 'newer', near),
    present: () => page('', 'present'),
    schedule() {
      if (!alive) return
      const value = hooks.current()
      clearTimeout(postTimer)
      postTimer = setTimeout(() => { if (selected(value)) load() }, 150)
    },
    start() {
      if (!alive) return
      clearInterval(timer)
      timer = setInterval(() => {
        const value = hooks.current()
        if (!value.id || !value.live || value.visible === false || polling) return
        polling = true
        load().finally(() => (polling = false))
      }, 1500)
    },
    destroy() { alive = false; clearInterval(timer); clearTimeout(postTimer) },
  }
}

