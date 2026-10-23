// The look settings live in the webview's localStorage like the layout does:
// they are view state, so Go never sees them.
import { readLook, withChoice } from './look.ts'
import type { Look } from './look.ts'

const KEY = 'dyna.look'

// The choice types look.ts owns, read from the call it exposes.
type ChoiceID = Parameters<typeof withChoice>[1]
type ChoiceValue = Parameters<typeof withChoice>[2]

function load(): Look {
  try {
    const raw = localStorage.getItem(KEY)
    const saved = readLook(raw)
    // An older version also saved per-view overrides. Drop them once, so the
    // stored look holds the choices for every feed view and nothing else.
    if (raw && raw !== JSON.stringify(saved)) localStorage.setItem(KEY, JSON.stringify(saved))
    return saved
  } catch {
    // No storage here, as in a test or a server render.
    return readLook(null)
  }
}

export const look = $state(load())

// Posts a jump asked for. They show even when their kind is hidden, until the app restarts.
export const revealed = $state<Record<string, boolean>>({})
export const reveal = (id: string) => { revealed[id] = true }

// choose sets one look choice for every feed view. A URL fixture still names its
// choice as plain text, so a kind can arrive untyped.
export function choose(kind: string, value: ChoiceValue) {
  look.all = withChoice(look, kind as ChoiceID, value).all
  try {
    localStorage.setItem(KEY, JSON.stringify({ all: look.all }))
  } catch {}
}
