// Pure rules for the floating computer preview and for when the Computer tab
// holds a stream. No DOM, so node tests can run them.

export const PHONE = 700 // px: below this width there is no floating preview

// previewVisible says whether the floating preview shows. It shows in the
// Messages view of a session whose computer runs or starts, or is stopped
// with a last picture. The owner can hide it; it comes back when the
// computer changes state.
export function previewVisible({ c, tab, view, width, hidden }) {
  if (!c || c.state === 'none' || c.state === 'idle') return false
  if (c.state === 'stopped' && !c.shotAt) return false
  if (tab !== 'chat' || view === 'computers') return false
  if (width < PHONE) return false
  return hidden?.state !== c.state
}

// inUse: the session's computer runs or starts, so its header icon shows.
export const inUse = (c) => !!c && (c.state === 'running' || c.state === 'starting')

// Which corner of a box (w by h) a point is closest to.
export function snapCorner(x, y, w, h) {
  return `${y < h / 2 ? 't' : 'b'}${x < w / 2 ? 'l' : 'r'}`
}

export const CORNERS = ['tl', 'tr', 'bl', 'br']
export const DEFAULT_PREVIEW = { corner: 'br', minimized: false, hidden: null }

const key = (session) => `dyna.computer.preview.${session}`

// loadPreview reads one session's saved choices. Bad data gives defaults.
export function loadPreview(session, store = globalThis.localStorage) {
  try {
    const v = JSON.parse(store?.getItem(key(session)) ?? 'null')
    return {
      corner: CORNERS.includes(v?.corner) ? v.corner : DEFAULT_PREVIEW.corner,
      minimized: v?.minimized === true,
      hidden: typeof v?.hidden?.state === 'string' ? { state: v.hidden.state } : null,
    }
  } catch {
    return { ...DEFAULT_PREVIEW }
  }
}

export function savePreview(session, p, store = globalThis.localStorage) {
  try {
    store?.setItem(key(session), JSON.stringify(p))
  } catch {}
}

// streamWanted says whether the Computer tab should hold a stream now. It
// never holds one for a computer with no window, or while the page is
// hidden, so leaving the tab or the window releases the viewer lease.
export const streamWanted = ({ tab, c, pageVisible }) => tab === 'computer' && pageVisible !== false && c?.state === 'running'

// Badge text over the picture while the stream is not live.
export function badgeText(phase, shown) {
  if (phase === 'connected') return shown ? 'Live' : ''
  if (phase === 'connecting') return 'Connecting…'
  if (phase === 'failed') return 'Could not connect'
  return 'Stream ended'
}

// freshDue says whether a visible picture should ask the daemon for a new
// screenshot now. It asks for a running computer, only while the page is
// visible, and at most once per `every` ms. The server limits real captures
// to one per 5 s per computer as well.
export const freshDue = ({ phase, visible, last, now, every }) => phase === 'running' && visible && now - last >= every
