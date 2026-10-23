import { memberChoices } from './mentions.ts'
import { createReloadGate } from '../ui/reloadGate.ts'

// Controllers own request identity and cleanup. App owns navigation and
// applies current results to its view state.
export function createRoomData(api, hooks) {
  let alive = true, roomsRequest = 0, membersRequest = 0, openRequest = 0
  const visible = (name) => alive && (!hooks.visible || hooks.visible(name))
  const reload = createReloadGate(() => hooks.current(), visible)
  const reloadRooms = createReloadGate(() => ({ room: 'rooms', visit: 0 }), () => visible('rooms'))
  const selected = (room, visit) => alive && room === hooks.current().room && visit === hooks.current().visit
  let roster = '', loadedChoices = '', rosterSelection = null
  const rosterKey = (members) => {
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
        if (current()) hooks.unread(value)
      } catch { /* Keep the last counts until the next read or room event. */ }
    }) },
    patchMembers(change) {
      return reload.patch('members', (current) => {
        if (!current()) return
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
        if (current() && alive && request === roomsRequest) hooks.rooms(rooms ?? [])
      } catch (e) { if (current() && alive && request === roomsRequest) hooks.error(String(e)) }
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
        hooks.opened(opened, initial)
        return true
      } catch (e) {
        if (selected(room, visit) && request === openRequest) hooks.error(String(e))
        return false
      }
    },
    destroy() { alive = false },
  }
}

