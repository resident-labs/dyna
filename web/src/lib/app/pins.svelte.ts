// The room's pinned posts: the rows the Pinned panel lists and the ids the feed
// marks. lib/rooms/Pins.svelte draws them; this module keeps the list and the
// requests that keep it current, so App.svelte only hosts them.
import { api } from '../../api.ts'
import { reconcileRows } from '../ui/reloadGate.ts'
import { router } from './router.svelte.ts'
import type { Post } from '../api/room.ts'

/** The page's own side of the pins. App binds these once. */
export interface PinsHost {
  /** The page's reload gate, so a list keeps one request and one refresh. */
  reload(name: string, load: (current: () => boolean) => void | Promise<unknown>): Promise<void>
  /** Show a failed read or write. */
  error(message: string): void
  /** A finished write clears the last error. */
  clearError(): void
}

// App binds the page's side during its script, before the first effect runs.
const missing = (name: string): never => { throw new Error(`pins: ${name} is unbound`) }
let pinsHost: PinsHost = {
  reload: () => missing('reload'),
  error: () => missing('error'),
  clearError: () => missing('clearError'),
}

export function bindPins(bound: PinsHost): void {
  pinsHost = bound
}

const roomId = $derived(router.roomId)

let pins = $state<Post[]>([]) // the room's pinned posts, newest pin first
let pinnedIds = $derived(new Set(pins.map((p) => p.id)))

function loadPins() {
  return pinsHost.reload('pins', async (current: () => boolean) => {
    if (!roomId) return
    const id = roomId
    try {
      const list = (await api.pins(id)) ?? []
      if (current() && id === roomId) pins = reconcileRows(pins, list)
    } catch (e) {
      if (current()) pinsHost.error(String(e))
    }
  })
}

// togglePin pins the post, or takes its pin away.
async function togglePin(p: Post) {
  try {
    if (pinnedIds.has(p.id)) await api.unpin(roomId, p.id)
    else await api.pin(roomId, p.id)
    pinsHost.clearError()
    loadPins()
  } catch (e) {
    pinsHost.error(String(e))
  }
}

/** The room's pins, named as the page named them when they lived there. */
export const pinView = {
  get pins() { return pins },
  get ids() { return pinnedIds },
  loadPins,
  togglePin,
  /** An opened room brings its pins. */
  setPins(rows: Post[]) { pins = rows },
  /** A room pick drops the old room's pins. */
  clear() { pins = [] },
}
