import { createReloadGate } from '../ui/reloadGate.js'

// Read writes share the reload gate, but retain IDs: a trailing batch must
// include every newly visible post, rather than only the last caller's list.
export function createReadMarker(api, hooks) {
  let visit = '', pending = new Map(), known = new Set()
  const reload = createReloadGate(hooks.current, () => hooks.current().visible)
  return {
    mark(posts) {
      const now = hooks.current()
      if (!now.room || !now.visible) { visit = ''; pending = new Map(); known = new Set(); return null }
      const selected = now.room + ':' + now.visit
      if (visit !== selected) { visit = selected; pending = new Map(); known = new Set() }
      const rooms = new Set()
      for (const post of posts) {
        const room = post.roomId || now.room, key = room + ':' + post.id
        if (!post.unread || known.has(key)) continue
        known.add(key)
        if (!pending.has(room)) pending.set(room, new Map())
        pending.get(room).set(post.id, post)
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
      return work.length ? Promise.all(work) : null
    },
  }
}
