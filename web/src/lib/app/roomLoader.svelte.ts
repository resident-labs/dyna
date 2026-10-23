// The open room's data: its feed, its member tree and the requests that keep
// them current. lib/rooms/roomData.ts makes the calls and owns their identity,
// lib/rooms/feedPages.ts joins a page to the feed on screen and
// lib/rooms/feedWindow.ts trims it. This module holds the state the page shows
// and connects those parts, so App.svelte only hosts the screen.
import { api } from '../../api.ts'
import { createRoomData } from '../rooms/roomData.ts'
import { inRoomFeed } from '../rooms/feedViews.ts'
import { mergeFeedPage, rerouteAnchors, withViews } from '../rooms/feedPages.ts'
import { trimFeed } from '../rooms/feedWindow.ts'
import { patchListRows } from '../ui/listChanges.ts'
import { reconcileRows } from '../ui/reloadGate.ts'
import { router } from './router.svelte.ts'
import type { RowChange } from '../ui/listChanges.ts'
import type { Feed, Member, Opened, Room } from '../api/room.ts'
import type { ListChange, PostsEvent } from '../api/event.ts'

// SavedFeed is the feed the page keeps: the server's page plus the client's history flag.
export type SavedFeed = Feed & { historyLoaded?: boolean }

// One page load in flight: a second request in the same room waits for it.
interface EarlierLoad {
  id: string
  roomLoad: number
  direction: string
  promise: Promise<boolean> | null
}

/** The page's own side of the room data. App binds these once. */
export interface RoomLoaderHost {
  /** The lists the page shows; a hidden list is not read. */
  visible(name: string): boolean
  /** The view name this room shows for v. */
  resolveView(v: string): string
  /** Closed sessions show in the member list. */
  closedShown(): boolean
  /** The reader is at the bottom of the feed. */
  atBottom(): boolean
  /** Leave the bottom of the feed, so a live post waits in the newer count. */
  leaveBottom(): void
  /** Show a failed load. */
  error(message: string): void
  /** The all-rooms list arrived. */
  rooms(value: Room[]): void
  /** The rest of an opened room, which the page owns. */
  opened(value: Opened): void
  /** Keep the picked sessions that still exist. */
  picked(rows: Member[]): void
  /** A changed roster moves the sessions' computers. */
  loadComputers(): void
  /** The room counts changed, so the room list reads again. */
  refreshCounts(): void
}

// App binds the page's side in its script, before the first effect runs, so no
// call reaches these first.
const missing = (name: string): never => { throw new Error(`roomLoader: ${name} is unbound`) }
let page: RoomLoaderHost = {
  visible: () => missing('visible'),
  resolveView: () => missing('resolveView'),
  closedShown: () => missing('closedShown'),
  atBottom: () => missing('atBottom'),
  leaveBottom: () => missing('leaveBottom'),
  error: () => missing('error'),
  rooms: () => missing('rooms'),
  opened: () => missing('opened'),
  picked: () => missing('picked'),
  loadComputers: () => missing('loadComputers'),
  refreshCounts: () => missing('refreshCounts'),
}

export function bindRoomLoader(bound: RoomLoaderHost): void {
  page = bound
}

let feed = $state<SavedFeed | null>(null)
let members = $state<Member[]>([])
let hierarchy = $state<Member[]>([])
let choices = $state<string[]>([])
let unreadFrom = $state('')
let roomRequest = $state(0) // room data can still load after a later view choice
// Feed views come from the server with each post, so a parent change (a
// promote) rereads the routing of every loaded post. reconcileRows updates
// rows in place, so the last key is kept, not recomputed.
let routedParents = ''
let earlierLoad: EarlierLoad | null = null
const STEP = 50
const roomId = $derived(router.roomId) // the room whose data this is

function memberRoster(list: Member[]) {
  return JSON.stringify(list.filter((m) => !m.owner).map((m) => [m.id, m.name, !!m.closed]).sort())
}
function parentKey(list: Member[]) {
  return JSON.stringify([roomId, list.filter((m) => !m.owner).map((m) => [m.id, m.parent || '']).sort()])
}
// noteParents records the member parent tree. When it changed in the same
// room, loaded posts reread their views. Live member updates and room opens,
// including a reconnect, both come through here.
function noteParents(rows: Member[]) {
  const parents = parentKey(rows)
  const moved = routedParents !== parents && JSON.parse(routedParents || '[null]')[0] === roomId
  routedParents = parents
  if (moved) rerouteLoaded()
}
async function rerouteLoaded() {
  const id = roomId, request = roomRequest
  if (!feed?.posts?.length) return
  try {
    const pages = await Promise.all(rerouteAnchors(feed.posts).map((post) => api.feedAround(id, post, 200)))
    if (id === roomId && request === roomRequest && feed) feed.posts = withViews(feed.posts ?? [], pages)
  } catch { /* The next room open or reconnect reroutes the posts. */ }
}
function roomCountKey(list: Member[]) {
  return JSON.stringify(list.filter((m) => !m.owner && !m.closed).map((m) => [m.id, m.state === 'live']).sort())
}
function applyMembers(value: Member[]) {
  const changed = memberRoster(hierarchy) !== memberRoster(value)
  const counts = roomCountKey(hierarchy)
  showMemberRows(reconcileRows(hierarchy, value), changed, counts)
}
function applyMemberItem(change: ListChange) {
  const before = memberRoster(hierarchy)
  const counts = roomCountKey(hierarchy)
  const known = hierarchy.some((m) => (m.id || m.address) === change.id)
  const rows = patchListRows(hierarchy, change as RowChange<Member>, (member) => member.id || member.address)
  showMemberRows(rows, before !== memberRoster(rows), known ? counts : null)
  return hierarchy
}
function showMemberRows(rows: Member[], rosterChanged: boolean, previousCounts: string | null) {
  noteParents(rows)
  hierarchy = rows
  page.picked(hierarchy)
  members = hierarchy.filter((m) => page.closedShown() || !m.closed)
  router.view = page.resolveView(router.view)
  if (rosterChanged) page.loadComputers()
  if (previousCounts !== roomCountKey(rows)) page.refreshCounts()
}

// The unread refresh carries view counts into the open feed.
function applyUnread(value: Feed) {
  if (feed?.room.id !== value.room.id) return
  feed.viewUnread = value.viewUnread ?? {}
}

// applyOpened shows what api.openRoom returned for the open room: the feed
// joins the one on screen and the member tree replaces the roster.
function applyOpened(o: Opened) {
  const f = mergeFeedPage(feed && !page.atBottom() ? { ...feed, historyLoaded: true } : feed, o.feed)
  f.posts = reconcileRows(feed && feed.room.id === f.room.id ? feed.posts ?? [] : [], f.posts ?? [])
  feed = f
  hierarchy = o.members ?? []
  noteParents(hierarchy)
  page.picked(hierarchy)
  router.view = page.resolveView(router.view)
  members = hierarchy.filter((m) => page.closedShown() || !m.closed)
  choices = o.choices ?? []
  page.opened(o)
}

const data = createRoomData(api, {
  current: () => ({ room: roomId, visit: roomRequest }),
  visible: (name: string) => page.visible(name),
  choices: (value: string[]) => { choices = value },
  rooms: (value: Room[]) => page.rooms(value),
  unread: applyUnread,
  members: applyMembers,
  patchMembers: (change: ListChange) => applyMemberItem(change),
  opened: (value: Opened, initial: boolean) => {
    if (initial) unreadFrom = value.feed.firstUnread ?? (value.feed.posts ?? []).find((p) => p.unread)?.id ?? ''
    applyOpened(value)
  },
  error: (message: string) => page.error(message),
})

// Each request adds one bounded older page. Existing posts keep their objects
// and unread marks until the room is left.
async function loadEarlier(direction = 'older', target = '', near = () => true): Promise<boolean> {
  const id = roomId
  const roomLoad = roomRequest
  if (earlierLoad?.id === id && earlierLoad.roomLoad === roomLoad) {
    if (earlierLoad.direction === direction) return earlierLoad.promise ?? false
    return earlierLoad.promise?.then(() => id === roomId && roomLoad === roomRequest && loadEarlier(direction, target, near)) ?? false
  }
  if (!feed || (direction === 'older' && !feed.earlier) || (direction === 'newer' && !feed.newer)) return false
  page.leaveBottom()
  const cursor = direction === 'older' ? feed.cursor ?? '' : direction === 'newer' ? feed.posts?.at(-1)?.id ?? '' : target ?? ''
  const request: EarlierLoad = { id, roomLoad, direction, promise: null }
  earlierLoad = request
  request.promise = (async () => {
    try {
      const page = direction === 'newer' ? await api.feedAfter(id, cursor, STEP)
        : direction === 'around' ? await api.feedAround(id, cursor, STEP)
        : await api.feedTail(id, direction === 'present' ? '' : cursor, STEP)
      if (id !== roomId || roomLoad !== roomRequest || !feed) return false
      if (!near()) return true
      if (direction === 'older' && feed.cursor !== cursor) return false
      feed = direction === 'around' ? { ...page, historyLoaded: true } : Reflect.apply(mergeFeedPage, undefined, [feed, page, direction]) as SavedFeed
      return true
    } catch (e) {
      if (id === roomId && roomLoad === roomRequest) page.error(String(e))
      return false
    } finally {
      if (earlierLoad === request) earlierLoad = null
    }
  })()
  return request.promise
}

// A live page of posts joins the open room feed. A live post written in a DM
// belongs to that DM, so it never joins the room feed.
function applyPosts(ev: PostsEvent) {
  if (!feed) return
  const seen = new Set((feed.posts ?? []).map((p) => p.id))
  const add = (ev.posts ?? []).filter((p) => !seen.has(p.id) && inRoomFeed(p))
  if (add.length) {
    if (feed.newer || !page.atBottom()) feed = { ...feed, newer: (feed.newer ?? 0) + add.length }
    else {
      feed.posts = [...(feed.posts ?? []), ...add]
      if (page.atBottom()) feed = trimFeed(feed)
    }
  }
}

// A new view opens at the bottom, so older posts can go. Selecting the open
// view again keeps the reader's place, so it trims only at the bottom.
function trimAtView(next: string, previous: string) {
  if (next !== previous || page.atBottom()) feed = trimFeed(feed)
}

/** The open room's data, named as the page named it when it lived there. */
export const roomData = {
  get feed() { return feed },
  get members() { return members },
  get hierarchy() { return hierarchy },
  get choices() { return choices },
  get unreadFrom() { return unreadFrom },
  get roomRequest() { return roomRequest },
  loadRooms() { return data.loadRooms() },
  loadMembers() { return data.loadMembers() },
  loadMentions() { return data.loadMentions() },
  loadUnread() { return data.loadUnread() },
  open(keep = 0, initial = false) { return data.open(keep, initial) },
  patchMembers(change: ListChange) { return data.patchMembers(change) },
  loadEarlier,
  applyPosts,
  trimAtView,
  /** A room selection starts a new visit, so a later answer cannot land. */
  newVisit() { roomRequest++ },
  /** A room selection drops the old room's rows until its answer arrives. */
  clearMembers() { members = []; hierarchy = [] },
  /** The all-rooms page shows no feed. */
  clearFeed() { feed = null },
  /** A page load leaves the bottom of the feed. */
  leaveBottom() { page.leaveBottom() },
  destroy() { data.destroy() },
}
