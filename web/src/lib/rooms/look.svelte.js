// The look settings live in the webview's localStorage like the layout does:
// they are view state, so Go never sees them.
import { withChoice } from './look.ts'

const KEY = 'dyna.look'

function load() {
  try {
    const s = JSON.parse(localStorage.getItem(KEY) || '{}')
    return { all: s?.all ?? {}, channels: s?.channels ?? {} }
  } catch {
    return { all: {}, channels: {} }
  }
}

export const look = $state(load())

// Posts a jump asked for. They show even when their kind is hidden, until the app restarts.
export const revealed = $state({})
export const reveal = (id) => { revealed[id] = true }

// choose sets one toggle for a channel scope, or for every channel when scope is null.
export function choose(scope, kind, value) {
  const next = withChoice(look, scope, kind, value)
  look.all = next.all
  look.channels = next.channels
  try {
    localStorage.setItem(KEY, JSON.stringify({ all: look.all, channels: look.channels }))
  } catch {}
}
