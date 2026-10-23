import { memberChoices } from './mentions.ts'
import { createReloadGate } from '../ui/reloadGate.ts'
import type { ListChange } from '../api/event.ts'
import type { Feed, Member, Opened, Room } from '../api/room.ts'

// The app's current room and visit. A load answers only while both are current.
export interface RoomSelection {
  room: string
  visit: number
}

// The calls the controller makes.
export interface RoomDataApi {
  mentions(room: string): Promise<string[] | null>
  feedTail(room: string, before: string, keep: number): Promise<Feed>
  rooms(): Promise<Room[] | null>
  members(room: string, withClosed: boolean): Promise<Member[] | null>
  openRoom(room: string, tail: number, withClosed: boolean): Promise<Opened>
}

// What the controller hands back to the app.
export interface RoomDataHooks {
  current(): RoomSelection
  members(members: Member[]): void
  choices(choices: string[]): void
  visible?(name: string): boolean
  rooms?(rooms: Room[]): void
  unread?(value: Feed): void
  patchMembers?(change: ListChange): Member[]
  opened?(value: Opened, initial: boolean): void
  error?(message: string): void
}

// Controllers own request identity and cleanup. App owns navigation and
// applies current results to its view state.
export function createRoomData(host: unknown, hooks: RoomDataHooks) {
  // api.js is plain JavaScript and test harnesses pass a bare record, so the
  // calls arrive untyped. Read them through the declared shape once here.
  const api = host as RoomDataApi
  let alive = true, roomsRequest = 0, membersRequest = 0, openRequest = 0
  const visible = (name: string) => alive && (!hooks.visible || hooks.visible(name))
  const reload = createReloadGate(() => hooks.current(), visible)
  const reloadRooms = createReloadGate(() => ({ room: 'rooms', visit: 0 }), () => visible('rooms'))
  const selected = (room: string, visit: number) => alive && room === hooks.current().room && visit === hooks.current().visit
  let roster = '', loadedChoices = '', rosterSelection: RoomSelection | null = null
  const rosterKey = (members: Member[]) => {
    const { room, visit } = hooks.current()
    rosterSelection = { room, visit }
    return JSON.stringify([room, visit, members.filter((m) => !m.owner && !m.closed).map((m) => m.name).sort()])
  }
  function loadMentions() { return reload('mentions', async (current) => {
    const key = roster
    if (!rosterSelection || !selected(rosterSelection.room, rosterSelection.visit) || key === loadedChoices) return
    try {
      const choices = await api.mentions(hooks.current().room)
      if (!current() || key !== roster) return
      loadedChoices = key
      hooks.choices(choices ?? [])
    } catch { /* Keep the current suggestions until the next refresh. */ }
  }) }
  return {
    loadMentions,
    loadUnread() { return reload('unread', async (current) => {
      const { room } = hooks.current()
      if (!alive || !room) return
      try {
        const value = await api.feedTail(room, '', 1)
        if (current()) hooks.unread?.(value)
      } catch { /* Keep the last counts until the next read or room event. */ }
    }) },
    patchMembers(change: ListChange) {
      return reload.patch('members', (current) => {
        if (!current() || !hooks.patchMembers) return
        const members = hooks.patchMembers(change)
        roster = rosterKey(members)
        loadedChoices = roster
        hooks.choices(memberChoices(members))
      })
    },
    loadRooms() { return reloadRooms('rooms', async (current) => {
      if (!alive) return
      const request = ++roomsRequest
      try {
        const rooms = await api.rooms()
        if (current() && alive && request === roomsRequest) hooks.rooms?.(rooms ?? [])
      } catch (e) { if (current() && alive && request === roomsRequest) hooks.error?.(String(e)) }
    }) },
    loadMembers() { return reload('members', async (current) => {
      const { room, visit } = hooks.current(), request = ++membersRequest
      if (!alive || !room) return
      try {
        const members = await api.members(room, true)
        if (!current() || !selected(room, visit) || request !== membersRequest) return
        roster = rosterKey(members ?? [])
        hooks.members(members ?? [])
        await loadMentions()
      } catch { /* Keep the current list and selection until a later refresh. */ }
    }) },
    async open(keep = 0, initial = false) {
      const { room, visit } = hooks.current(), request = ++openRequest
      if (!alive || !room) return false
      try {
        const opened = await api.openRoom(room, keep, true)
        if (!selected(room, visit) || request !== openRequest) return false
        roster = rosterKey(opened.members ?? [])
        loadedChoices = roster
        hooks.opened?.(opened, initial)
        return true
      } catch (e) {
        if (selected(room, visit) && request === openRequest) hooks.error?.(String(e))
        return false
      }
    },
    destroy() { alive = false },
  }
}
