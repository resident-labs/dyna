// Tests that slice App.svelte run its functions in one flat vm state. The open
// room's data now lives in lib/app/roomLoader.svelte.ts, so the same tests run
// the loader's functions there too. This helper installs them over a test state,
// gives that state the `roomData` object App reads, and wires one room data
// controller to the slice, so the state stays the single source of truth.
import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import { moduleFunctions } from './svelte-source.ts'
import { installRouterShim } from './appRouterShim.ts'
import { createRoomData } from '../src/lib/rooms/roomData.ts'
import type { Opened, Member } from '../src/lib/api/room.ts'
import type { ListChange } from '../src/lib/api/event.ts'
import { inRoomFeed } from '../src/lib/rooms/feedViews.ts'
import { mergeFeedPage, rerouteAnchors, withViews } from '../src/lib/rooms/feedPages.ts'
import { trimFeed } from '../src/lib/rooms/feedWindow.ts'
import { patchListRows } from '../src/lib/ui/listChanges.ts'
import { reconcileRows } from '../src/lib/ui/reloadGate.ts'

const source = readFileSync(new URL('../src/lib/app/roomLoader.svelte.ts', import.meta.url), 'utf8')

/** The loader's own functions, which a vm can run in a flat state. */
export const LOADER_FUNCTIONS = [
  'memberRoster', 'parentKey', 'noteParents', 'rerouteLoaded', 'roomCountKey', 'applyMembers',
  'applyMemberItem', 'showMemberRows', 'applyUnread', 'applyOpened', 'loadEarlier', 'applyPosts', 'trimAtView',
] as const

/** The fields of the room data that App reads through roomData. */
export const ROOM_DATA_STATE = ['feed', 'members', 'hierarchy', 'choices', 'unreadFrom', 'roomRequest'] as const

type State = Record<string, unknown>

const get = (state: State, name: string): unknown => Reflect.get(state, name)
const set = (state: State, name: string, value: unknown): void => { Reflect.set(state, name, value) }
const fn = (state: State, name: string): ((...args: never[]) => unknown) => {
  const value = get(state, name)
  return typeof value === 'function' ? value as (...args: never[]) => unknown : () => undefined
}

/** call runs a loader function installed in the state. */
export function callLoader<T>(state: object, name: string, ...args: unknown[]): T {
  const slice = fn(state as State, name) as (...values: unknown[]) => T
  return slice(...args)
}

// page is the loader's side of the page: the state's own fields answer it.
function installPage(state: State): void {
  set(state, 'page', {
    visible: (name: string) => get(state, 'visibleList') === undefined ? true : (fn(state, 'visibleList') as (n: string) => boolean)(name),
    resolveView: (v: string) => (get(state, 'feedViews') as { resolveView(v: string): string }).resolveView(v),
    closedShown: () => !!get(state, 'showClosed'),
    atBottom: () => !!get(state, 'feedAtBottom'),
    leaveBottom: () => set(state, 'feedAtBottom', false),
    error: (message: string) => set(state, 'loadError', message),
    rooms: (rooms: unknown) => set(state, 'rooms', rooms),
    opened: (value: unknown) => { if (get(state, 'applyRoomOpened') !== undefined) (fn(state, 'applyRoomOpened') as (v: unknown) => void)(value) },
    picked: (rows: unknown[]) => {
      if (get(state, 'setPicked') === undefined) return
      const prune = get(state, 'pruneSelection')
      ;(fn(state, 'setPicked') as (rows: unknown[]) => void)(typeof prune === 'function' ? (prune as (picked: unknown, rows: unknown[]) => string[])(get(state, 'pickedSessions'), rows) : rows)
    },
    loadComputers: () => { if (get(state, 'loadComputers') !== undefined) (fn(state, 'loadComputers') as () => void)() },
    refreshCounts: () => { if (get(state, 'soonRooms') !== undefined) (fn(state, 'soonRooms') as () => void)() },
  })
}

/**
 * installRoomLoader runs the loader's functions in the state and gives it the
 * roomData object App reads: the state's own fields, plus a room data
 * controller wired to the same slice.
 */
export function installRoomLoader(state: object, api: unknown): void {
  const fields = state as State
  if (!vm.isContext(state)) vm.createContext(state)
  installRouterShim(state)
  for (const name of ['mergeFeedPage', 'rerouteAnchors', 'withViews', 'trimFeed', 'inRoomFeed', 'patchListRows', 'reconcileRows']) {
    if (get(fields, name) === undefined) set(fields, name, { mergeFeedPage, rerouteAnchors, withViews, trimFeed, inRoomFeed, patchListRows, reconcileRows }[name])
  }
  for (const [, code] of moduleFunctions(source, LOADER_FUNCTIONS)) vm.runInContext(code, state as vm.Context)
  installPage(fields)
  const data = createRoomData(api, {
    current: () => ({ room: String(get(fields, 'roomId') ?? ''), visit: Number(get(fields, 'roomRequest') ?? 0) }),
    visible: (name: string) => (get(fields, 'page') as { visible(n: string): boolean }).visible(name),
    choices: (value: string[]) => set(fields, 'choices', value),
    rooms: (value: unknown) => (get(fields, 'page') as { rooms(v: unknown): void }).rooms(value),
    unread: (value: unknown) => callLoader(fields, 'applyUnread', value),
    members: (value: unknown) => callLoader(fields, 'applyMembers', value),
    patchMembers: (change: ListChange) => callLoader<Member[]>(fields, 'applyMemberItem', change),
    opened: (value: Opened, initial: boolean) => {
      if (initial) set(fields, 'unreadFrom', value.feed.firstUnread ?? (value.feed.posts ?? []).find((p) => p.unread)?.id ?? '')
      callLoader(fields, 'applyOpened', value)
    },
    error: (message: string) => set(fields, 'loadError', message),
  })
  const shim: Record<string, unknown> = {
    loadRooms: () => data.loadRooms(),
    loadMembers: () => data.loadMembers(),
    loadMentions: () => data.loadMentions(),
    loadUnread: () => data.loadUnread(),
    open: (keep?: number, initial?: boolean) => data.open(keep, initial),
    patchMembers: (change: ListChange) => data.patchMembers(change),
    destroy: () => data.destroy(),
    loadEarlier: (...args: unknown[]) => callLoader(fields, 'loadEarlier', ...args),
    applyPosts: (event: unknown) => callLoader(fields, 'applyPosts', event),
    trimAtView: (next: string, previous: string) => callLoader(fields, 'trimAtView', next, previous),
    newVisit: () => set(fields, 'roomRequest', Number(get(fields, 'roomRequest') ?? 0) + 1),
    clearMembers: () => { set(fields, 'members', []); set(fields, 'hierarchy', []) },
    clearFeed: () => set(fields, 'feed', null),
    leaveBottom: () => (get(fields, 'page') as { leaveBottom(): void }).leaveBottom(),
  }
  for (const name of ROOM_DATA_STATE) {
    Object.defineProperty(shim, name, {
      enumerable: true,
      get: () => get(fields, name),
      set: (value: unknown) => set(fields, name, value),
    })
  }
  set(fields, 'roomData', shim)
}
