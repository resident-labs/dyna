// Pure helpers for the Computer screens. No DOM, no network, so node tests
// can run them. The view model comes from App.Computers (see
// internal/computerweb): {session, name, state, apps:[{appId,title}], width,
// height, control, viewers, stream:{running,codec,encoder}, rssKiB,
// streamRssKiB, shotAt, startedAt, ...}. state is none, idle, running or
// stopped.

export const isLive = (c) => c?.state === 'running'
export const hasComputer = (c) => !!c && c.state !== 'none'

const STATE_TEXT = {
  none: 'No desktop yet',
  idle: 'Ready · no window yet',
  starting: 'Starting…',
  running: 'Running',
  stopped: 'Stopped',
}

export function stateText(c) {
  if (c?.state === 'stopped') return c.reason ? `Stopped · ${c.reason}` : 'Stopped'
  return STATE_TEXT[c?.state] ?? STATE_TEXT.none
}

// ageText: "4 s ago", "3 min ago". A missing time says so.
export function ageText(iso, now = Date.now()) {
  if (!iso) return 'no picture yet'
  const s = Math.max(0, Math.round((now - Date.parse(iso)) / 1000))
  if (s < 60) return `${s} s ago`
  if (s < 3600) return `${Math.round(s / 60)} min ago`
  if (s < 86400) return `${Math.round(s / 3600)} h ago`
  return `${Math.round(s / 86400)} d ago`
}

// uptimeText: "up 12 min", "up 1 h 4 min". Empty without a start time.
export function uptimeText(iso, now = Date.now()) {
  if (!iso) return ''
  const min = Math.max(0, Math.floor((now - Date.parse(iso)) / 60000))
  if (min < 1) return 'up under 1 min'
  if (min < 60) return `up ${min} min`
  const h = Math.floor(min / 60)
  return `up ${h} h${min % 60 ? ` ${min % 60} min` : ''}`
}

export function mib(kib) {
  const m = (kib || 0) / 1024
  return `${m >= 100 || Number.isInteger(m) ? Math.round(m) : m.toFixed(1)} MiB`
}

// appName turns an app ID such as "org.mozilla.firefox" into "Firefox".
export function appName(id) {
  const last = String(id || '').split('.').pop().replace(/[-_]+/g, ' ').trim()
  return last ? last[0].toUpperCase() + last.slice(1) : ''
}

// streamLine is the tile's "Stream" value.
export function streamLine(c) {
  const s = c?.stream
  if (!s?.running) return `off · ${mib(0)}`
  return `${[s.codec, s.encoder].filter(Boolean).join(' · ') || 'on'} · ${mib(c.streamRssKiB)}`
}

// shotRatio is the CSS aspect-ratio of a computer's picture: its own
// width and height, 16 / 10 until the daemon reports a size.
export const shotRatio = (c) => (c?.width && c?.height ? `${c.width} / ${c.height}` : '16 / 10')

export const sizeText = (c) => (c?.width && c?.height ? `${c.width}×${c.height}` : '')

// Newest actions last; the feed keeps a few per session. Each item is
// {line, at}, with at in epoch milliseconds.
export const FEED_KEEP = 6
export function pushAction(feed, session, line, at = Date.now(), keep = FEED_KEEP) {
  if (!line) return feed
  const list = [...(feed[session] ?? []), { line, at }].slice(-keep)
  return { ...feed, [session]: list }
}

// openedApps: the names of apps in next that were not in prev. A session
// that had no list before reports nothing, so a first load is not an "open".
export function openedApps(prev, next) {
  if (!prev) return []
  const had = new Set(prev.map((a) => a.appId))
  return (next ?? []).filter((a) => !had.has(a.appId)).map((a) => appName(a.appId)).filter(Boolean)
}

// stateWord is the small-caps word at the left of the viewer's top line.
export const stateWord = (phase, human) => (human ? 'You are driving' : { connected: 'Live', connecting: 'Connecting', failed: 'No stream', ended: 'Stream ended' }[phase] ?? 'Live')

// The quiet hint at the right of the footer.
export const hintText = (human) => (human ? 'Press Esc twice to return control.' : 'Your clicks go nowhere until you take control.')

// escTwice is a tiny detector: feed it each Esc press time; it says true on
// the second press within 600 ms.
export function escTwice(gap = 600) {
  let last = -Infinity
  return (t) => {
    const hit = t - last <= gap
    last = hit ? -Infinity : t
    return hit
  }
}

// applyEvent returns the list after a "computer" event. A status in the
// event replaces that session's row (the room list keeps the name). An event
// for a session not in the list changes nothing: the room list owns who is
// in the room. needsReload is true when the event cannot be applied.
export function applyEvent(list, ev) {
  const i = list.findIndex((c) => c.session === ev.session)
  if (i < 0) return { list, needsReload: false }
  if (!ev.computer) return { list, needsReload: ev.type !== 'act' && ev.type !== 'shot' }
  const next = list.slice()
  next[i] = { ...ev.computer, name: list[i].name || ev.computer.name }
  return { list: next, needsReload: false }
}

// The list the room screens show, running first, then by name.
const order = { running: 0, starting: 0, idle: 1, stopped: 2, none: 3 }
export const sortComputers = (list) =>
  [...list].sort((a, b) => (order[a.state] ?? 3) - (order[b.state] ?? 3) || String(a.name).localeCompare(b.name))

// splitComputers sorts the list into picture tiles for live computers
// (running or starting) and one-line rows for the rest, so idle and stopped
// agents do not fill the page with black tiles.
export function splitComputers(list) {
  const sorted = sortComputers(list)
  const tile = (c) => c.state === 'running' || c.state === 'starting'
  return { tiles: sorted.filter(tile), rows: sorted.filter((c) => !tile(c)) }
}

// A new random key names one viewer. The server maps it to the daemon's
// viewer lease, so the page never sees the lease ID.
export function viewKey(bytes = 12) {
  const a = new Uint8Array(bytes)
  crypto.getRandomValues(a)
  return Array.from(a, (b) => b.toString(36).padStart(2, '0')).join('').slice(0, 24)
}

// popOut opens a computer in its own window at #pop=<room>:<session>. Root's
// computer has no session view, so the Computer tab opens it this way.
export function popOut(room, session) {
  const w = window.open(`${location.pathname}#pop=${encodeURIComponent(room)}:${encodeURIComponent(session)}`, `dyna-computer-${session}`, 'popup,width=1100,height=760')
  if (w) window.dispatchEvent(new CustomEvent('dyna:computer-popped', { detail: session }))
  return w
}

// computerLabel names a computer. The room's own root computer (session
// root-…) is "root (room)", apart from the root session's computer "root".
export const computerLabel = (c) => (c.session?.startsWith('root-') ? 'root (room)' : c.name)
