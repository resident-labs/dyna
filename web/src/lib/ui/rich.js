import { cardsIsolated } from '../../api.js'

// HTML cards run scripts only on the separate card origin.
export function gated() {
  return cardsIsolated()
}

export function sandbox() {
  return gated() ? 'allow-scripts' : ''
}

export const HEIGHT_DEFAULT = 360
export const HEIGHT_MIN = 120
export const HEIGHT_MAX = 12000

// cardHeight trusts no stored height: Go clamps it, and so does the client.
export function cardHeight(h) {
  const n = Number(h) || HEIGHT_DEFAULT
  return Math.min(HEIGHT_MAX, Math.max(HEIGHT_MIN, Math.round(n)))
}

// guard tracks a card frame's load events. A link click inside a card
// navigates the frame itself (the sandbox blocks the top page). WebKitGTK
// also reloads a sandboxed frame for an in-page #anchor, so anchors count as
// links. <details> and :checked controls fire no load. The first load is
// the card.
// The first navigation restores the card once; the load the restore causes
// is expected. A second navigation stops the card until the owner reloads
// it.
//
// States: 'loading' -> 'shown' on load. 'shown' + load -> 'restoring'
// (restore once) or 'stopped'. 'restoring' + load -> 'shown'.
export function guard() {
  return { state: 'loading', restores: 0 }
}

// onLoad returns the next guard and what the card must do: '' (nothing),
// 'restore' (reload the card URL) or 'stop' (remove the frame).
export function onLoad(g) {
  switch (g.state) {
    case 'loading':
      return [{ ...g, state: 'shown' }, '']
    case 'restoring':
      return [{ ...g, state: 'shown' }, '']
    case 'shown':
      if (g.restores === 0) return [{ ...g, state: 'restoring', restores: 1 }, 'restore']
      return [{ ...g, state: 'stopped' }, 'stop']
  }
  return [g, '']
}

// readCardMessage reads the two messages a card may send its parent:
//   {type: 'dyna:height', height: <px>}  resize the feed card
//   {type: 'dyna:open', url: 'https://…'} open a link in the owner's browser
// Anything else is ignored. The caller checks that the message came from
// that card's own frame.
export function readCardMessage(data) {
  if (data === null || typeof data !== 'object') return null
  if (data.type === 'dyna:height') {
    const h = Number(data.height)
    return Number.isFinite(h) ? { type: 'height', height: cardHeight(h) } : null
  }
  if (data.type === 'dyna:open' && typeof data.url === 'string' && data.url.length <= 2048) {
    try {
      const u = new URL(data.url)
      if (u.protocol === 'https:' || u.protocol === 'http:') return { type: 'open', url: u.href }
    } catch {
      return null
    }
  }
  return null
}

// frameKey names one loaded card document. The same bytes can be posted
// again with another network policy (dyna post --allow-net), and a loaded
// frame keeps the policy it loaded with, so the policy is part of the key.
export const frameKey = (att) => att.id + (att.net ? ':net' : ':closed')

// forKey is g for the frame named key. A new key is a new document (the
// frame was replaced), so it starts a fresh guard: its first load is the
// card, not a navigation.
export function forKey(g, key) {
  return g.key === key ? g : { ...guard(), key }
}
