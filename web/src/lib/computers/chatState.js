// The floating chat on the Computer tab. Pure rules, so node tests can run
// them. The chat has three shapes: tucked (one pill), composer (the pill
// widens into an input) and chat (the recent thread grows upward). While the
// owner drives, keys belong to the computer. The pill stays below
// the frame, and a click or Ctrl+/ reaches the chat.
import { plain } from '../sessions/dm.ts'

export const TUCKED = 'tucked'
export const COMPOSER = 'composer'
export const CHAT = 'chat'

// How many rows the chat window renders.
export const RECENT = 60


// A click on a part of the chat or the stream. `where` is pill, lastline,
// header or stream. The stream always tucks the chat.
export function chatClick(state, where) {
  if (where === 'stream' || where === 'header') return TUCKED
  if (where === 'pill') return CHAT
  if (where === 'lastline') return CHAT
  return state
}

// A key press. e: {key, ctrl, meta, alt, editable, inside}. editable: the
// target takes text. inside: the target is in the chat. Returns the next
// state and whether the chat took the key (the caller then stops it).
export function chatKey(state, e, driving) {
  const none = { state, handled: false }
  if (e.key === '/' && (e.ctrl || e.meta) && !e.alt) return { state: CHAT, handled: true }
  if (e.key === 'Escape') {
    // Driving: Esc belongs to the computer, unless the chat has the focus.
    if (driving && !e.inside) return none
    return state === TUCKED ? none : { state: TUCKED, handled: true }
  }
  if (e.key === '/' && !e.ctrl && !e.meta && !e.alt) {
    if (driving || e.editable) return none
    return state === TUCKED ? { state: COMPOSER, handled: true } : none
  }
  return none
}

// Taking control tucks the chat, so nothing sits on the stream.
export const chatDriving = (state, driving) => (driving ? TUCKED : state)

// lastLine identifies new agent activity for the unread dot.
// Returns {key, who, text}, or null when there is none.
export function lastLine(rows, address) {
  for (let i = (rows?.length ?? 0) - 1; i >= 0; i--) {
    const r = rows[i]
    if (r.kind === 'message' && r.author === address && r.text?.trim()) return { key: r.key, who: 'say', text: plain(r.text) }
    if (r.kind === 'activity') {
      const t = [...r.items].reverse().find((it) => it.kind === 'tool')
      if (t) return { key: r.key + ':' + r.items.length, who: 'act', text: '' }
    }
  }
  return null
}
