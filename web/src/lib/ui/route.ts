// The address of the open screen, so Back, Forward and reload return to it:
// #room=<id>, plus one of view=<view>, session=<id>[&turn=<n>] or run=<id>.
// The all-rooms page has no address. PR, issue and file links keep their own
// format (prlink.js); a completion link (details.js) names a session or run.
import { roomIDPattern } from './posts.js'
import { detailIDs } from './details.js'
import { GENERAL } from '../rooms/channels.js'

export interface Route {
  room: string
  view: string
  session: string
  turn: number
  run: string
}

// The root conversation has no session ID of its own, so its address says root.
export const ROOT_SESSION = 'root'
export const routeSession = (m: { id: string; rootSeat?: boolean } | null): string => (m ? (m.rootSeat ? ROOT_SESSION : m.id) : '')
const isSession = (id: string): boolean => id === ROOT_SESSION || detailIDs.session.test(id)
// A view is a lower-case name with hyphens, or a channel. routeHash writes
// only views that parseRoute reads back.
const VIEW = /^(?:[a-z][a-z-]{0,31}|channel:[A-Za-z0-9_.-]{1,96})$/

export function routeHash(r: Route): string {
  if (!roomIDPattern.test(r.room)) return ''
  const q = new URLSearchParams({ room: r.room })
  if (r.session) {
    q.set('session', r.session)
    if (r.turn > 0) q.set('turn', String(r.turn))
  } else if (r.run) q.set('run', r.run)
  else if (r.view !== GENERAL && VIEW.test(r.view)) q.set('view', r.view)
  return `#${q}`
}

// roomHash is the address of one room view, such as a list with no item open.
export const roomHash = (room: string, view: string): string => routeHash({ room, view, session: '', turn: 0, run: '' })

// parseRoute reads the screen an address names, or null when it names no
// room. Unknown or malformed parts fall back to the room's general view.
export function parseRoute(hash: string): Route | null {
  const q = new URLSearchParams(hash.replace(/^#/, ''))
  const room = q.get('room') ?? ''
  if (!roomIDPattern.test(room)) return null
  const detail = q.get('dyna-detail')
  const id = q.get('id') ?? ''
  const session = detail === 'session' ? id : q.get('session') ?? ''
  const run = detail === 'run' ? id : q.get('run') ?? ''
  const turn = Number(q.get('turn') ?? 0)
  const view = q.get('view') ?? GENERAL
  if (isSession(session)) return { room, view: GENERAL, session, turn: Number.isSafeInteger(turn) && turn > 0 ? turn : 0, run: '' }
  if (detailIDs.run.test(run)) return { room, view: 'runs', session: '', turn: 0, run }
  return { room, view: VIEW.test(view) ? view : GENERAL, session: '', turn: 0, run: '' }
}

// writeRoute puts the route's address in the address bar. A new screen gets
// a new history entry; replace corrects the current entry instead.
export function writeRoute(hash: string, replace: boolean, loc: Pick<Location, 'hash' | 'pathname' | 'search'>, hist: Pick<History, 'pushState' | 'replaceState'>): void {
  if (loc.hash === hash || (!loc.hash && !hash)) return
  const url = loc.pathname + loc.search + hash
  if (replace) hist.replaceState(null, '', url)
  else hist.pushState(null, '', url)
}
