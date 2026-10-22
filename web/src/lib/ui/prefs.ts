import { writable } from 'svelte/store'
import { isRecord } from '../api/guards.ts'

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

// channelList is how the room sidebar lists channels: "all" shows every
// agent channel, "folded" keeps #general and folds the rest into one "More
// channels" row, and "messages" replaces them all with one Messages row that
// opens #general.
export const CHANNEL_LISTS: [string, string][] = [['all', 'All channels'], ['folded', 'Folded'], ['messages', 'Messages only']]

export function channelListOf(saved: Record<string, unknown>) {
  const value = saved.channelList
  return typeof value === 'string' && CHANNEL_LISTS.some(([v]) => v === value) ? value : 'all'
}

export const channelList = writable(channelListOf(read()))

export function saveChannelList(value: string) {
  saveLayout({ channelList: value })
  channelList.set(value)
}

// lastRoom is the room the owner had open: {room, view}, or null when none
// was saved. room is '' for the all-rooms page.
// The views a room reopens on: the feed filters and the workspace views.
const VIEWS = ['general', 'files', 'github', 'issues', 'cihealth', 'library', 'terminal', 'jobs']

const viewOrGeneral = (view: unknown) => (typeof view === 'string' && (VIEWS.includes(view) || /^channel:[^\s]+$/.test(view)) ? view : 'general')

export function lastRoom() {
  const s = read()
  if (typeof s.room !== 'string') return null
  return { room: s.room, view: viewOrGeneral(s.view) }
}

export function saveLastRoom(room: string, view: string) {
  saveLayout({ room, view: viewOrGeneral(view) })
}
