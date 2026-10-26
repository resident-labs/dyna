import { createReloadGate } from '../ui/reloadGate.ts'

// The feed the reader sees now: its room, its visit and whether it is visible.
export interface ReadSelection {
  room: string
  visit: string | number
  visible?: boolean
  // chat is true when the visible feed is the room chat, the one the rail's
  // badge counts.
  chat?: boolean
}

// The fields a read marker needs from a post.
export interface ReadPost {
  id: string
  roomId?: string
  unread?: boolean
}

// The host call that marks posts read.
export interface ReadMarkerApi {
  markReadMany(room: string, ids: string[]): Promise<unknown>
  // markRoomRead marks every unread room-chat post read, loaded or not.
  markRoomRead(room: string): Promise<unknown>
}

// What the marker hands back to the app.
export interface ReadMarkerHooks {
  current(): ReadSelection
  read(): void
}

// Read writes share the reload gate, but retain IDs: a trailing batch must
// include every newly visible post, rather than only the last caller's list.
export function createReadMarker(api: ReadMarkerApi, hooks: ReadMarkerHooks) {
  let visit = ''
  const pending = new Map<string, Map<string, ReadPost>>()
  const known = new Set<string>()
  // cleared is the visit whose room-chat badge is already cleared.
  let cleared = ''
  const reload = createReloadGate(hooks.current, () => hooks.current().visible === true)
  return {
    mark(posts: ReadPost[]): Promise<unknown> | null {
      const now = hooks.current()
      if (!now.room || !now.visible) { visit = ''; cleared = ''; pending.clear(); known.clear(); return null }
      const selected = now.room + ':' + now.visit
      if (visit !== selected) { visit = selected; pending.clear(); known.clear() }
      // Viewing the room chat clears its badge, even for posts never loaded.
      let clear: Promise<void> | null = null
      if (now.chat && cleared !== selected) {
        cleared = selected
        clear = api.markRoomRead(now.room).then(() => { if (hooks.current().room === now.room) hooks.read() }, () => { if (cleared === selected) cleared = '' })
      }
      const rooms = new Set<string>()
      for (const post of posts) {
        const room = post.roomId || now.room, key = room + ':' + post.id
        if (!post.unread || known.has(key)) continue
        known.add(key)
        if (!pending.has(room)) pending.set(room, new Map())
        pending.get(room)?.set(post.id, post)
        rooms.add(room)
      }
      const work = [...rooms].map((room) => reload('read:' + room, async (current) => {
        const batch = pending.get(room)
        if (!batch?.size) return
        pending.delete(room)
        try {
          await api.markReadMany(room, [...batch.keys()])
          if (!current()) return
          for (const post of batch.values()) post.unread = false
          hooks.read()
        } catch {
          if (current()) for (const id of batch.keys()) known.delete(room + ':' + id)
        }
      }))
      if (clear) work.push(clear)
      return work.length ? Promise.all(work) : null
    },
  }
}
