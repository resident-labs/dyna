// reloadsUnread reports whether a live event needs the open room's unread
// counts loaded again (FeedTail). New posts do. A members event that carried
// its row was already patched in place, so only a whole-list members event
// does.
export interface RoomEvent {
  kind: string
  roomId?: string
}

export function reloadsUnread(ev: RoomEvent, roomId: string, patched: boolean): boolean {
  if (!roomId || ev.roomId !== roomId) return false
  return ev.kind === 'posts' || (ev.kind === 'members' && !patched)
}
