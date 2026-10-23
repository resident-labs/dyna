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
  // Later steps stay in the turn's first timeline row. Compare their own
  // times so a tool after a reply can still set the unread dot.
  const candidates = (rows ?? []).flatMap((row) => {
    if (row.kind === 'message' && row.author === address && row.text?.trim()) {
      return [{ line: { key: row.key, who: 'say', text: plain(row.text) }, ts: row.ts }]
    }
    if (row.kind === 'timeline') return row.steps.map((step) => ({
      line: { key: `${row.key}:${step.key}:${step.posts?.length ?? 0}`, who: 'act', text: '' },
      ts: step.posts?.at(-1)?.ts ?? step.ts ?? row.ts,
    }))
    return []
  })
  let last = null, latest = -1
  for (const { line, ts } of candidates) {
    const at = Math.max(0, (typeof ts === 'number' ? ts : Date.parse(ts ?? '')) || 0)
    if (at >= latest) { last = line; latest = at }
  }
  return last
}
