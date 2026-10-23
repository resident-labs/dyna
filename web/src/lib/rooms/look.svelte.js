// The look settings live in the webview's localStorage like the layout does:
// they are view state, so Go never sees them.
import { readLook, withChoice } from './look.ts'

const KEY = 'dyna.look'

function load() {
  try {
    const raw = localStorage.getItem(KEY)
    const saved = readLook(raw)
    // An older version also saved per-channel overrides. Drop them once, so the
    // stored look holds the choices for every channel and nothing else.
    if (raw && raw !== JSON.stringify(saved)) localStorage.setItem(KEY, JSON.stringify(saved))
    return saved
  } catch {
    // No storage here, as in a test or a server render.
    return readLook(null)
  }
}

export const look = $state(load())

// Posts a jump asked for. They show even when their kind is hidden, until the app restarts.
export const revealed = $state({})
export const reveal = (id) => { revealed[id] = true }

// choose sets one look choice for every channel.
export function choose(kind, value) {
  look.all = withChoice(look, kind, value).all
  try {
    localStorage.setItem(KEY, JSON.stringify({ all: look.all }))
  } catch {}
}
