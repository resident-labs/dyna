import { writable } from 'svelte/store'
import { isRecord } from '../api/guards.ts'
import { dockOf, type DockView } from '../sessions/termDock.ts'

// Layout preferences live in the webview's localStorage. They are view
// state only, so Go never sees them. A missing or broken store falls back
// to the defaults.
const KEY = 'dyna.layout'

function read(): Record<string, unknown> {
  try {
    const saved: unknown = JSON.parse(localStorage.getItem(KEY) || '{}')
    return isRecord(saved) ? saved : {}
  } catch {
    return {}
  }
}

export function loadLayout<T extends Record<string, unknown>>(defaults: T) {
  return { ...defaults, ...read() }
}

// saveLayout merges into what is stored, so the layout and the last room
// each save without erasing the other.
export function saveLayout(layout: Record<string, unknown>) {
  try {
    localStorage.setItem(KEY, JSON.stringify({ ...read(), ...layout }))
  } catch {}
}

export const showReasoning = writable(read().showReasoning !== false)

export function saveShowReasoning(value: boolean) {
  saveLayout({ showReasoning: value })
  showReasoning.set(value)
}

export const showCompaction = writable(read().showCompaction !== false)

export function saveShowCompaction(value: boolean) {
  saveLayout({ showCompaction: value })
  showCompaction.set(value)
}

// feedViewList is how the room sidebar lists feed views: "all" shows every
// agent view, "folded" keeps the room chat and folds the rest into one "More
// views" row, and "messages" replaces them all with one room chat row.
export const VIEW_LISTS: [string, string][] = [['all', 'All views'], ['folded', 'Folded'], ['messages', 'Room chat only']]

export function feedViewListOf(saved: Record<string, unknown>) {
  // TODO: legacy/shim remove later — the layout keeps its old key, so a
  // browser that saved a list before the feed view rename keeps it.
  const value = saved.channelList
  return typeof value === 'string' && VIEW_LISTS.some(([v]) => v === value) ? value : 'all'
}

export const feedViewList = writable(feedViewListOf(read()))

export function saveFeedViewList(value: string) {
  saveLayout({ channelList: value })
  feedViewList.set(value)
}

// lastRoom is the room the owner had open: {room, view}, or null when none
// was saved. room is '' for the all-rooms page.
// The views a room reopens on: the feed filters and the workspace views.
const VIEWS = ['general', 'files', 'github', 'issues', 'library', 'terminal', 'jobs', 'requests']

const viewOrGeneral = (view: unknown) => {
  // TODO(legacy/shim): keep old saved Questions views on the Requests page.
  if (view === 'questions') return 'requests'
  return typeof view === 'string' && (VIEWS.includes(view) || /^channel:[^\s]+$/.test(view)) ? view : 'general'
}

export function lastRoom() {
  const s = read()
  if (typeof s.room !== 'string') return null
  return { room: s.room, view: viewOrGeneral(s.view) }
}

export function saveLastRoom(room: string, view: string) {
  saveLayout({ room, view: viewOrGeneral(view) })
}

// The terminal dock's state (open, height, maximized, open shell) is kept per
// room on this device.
export function loadDock(room: string): DockView {
  const saved = read().terminalDock
  return dockOf(isRecord(saved) ? saved[room] : undefined)
}

export function saveDock(room: string, dock: DockView) {
  const saved = read().terminalDock
  saveLayout({ terminalDock: { ...(isRecord(saved) ? saved : {}), [room]: dock } })
}
