// The screen the page shows and the address that names it: the open room, its
// view, the session or run drilled into, and the review pane beside the feed.
// lib/ui/route.ts turns this state into an address and back; this module keeps
// it and the functions that change it, so App.svelte only hosts the screen.
import { GENERAL, isFeedView } from '../rooms/feedViews.ts'
import { routeHash, routeSession, writeRoute } from '../ui/route.ts'
import type { Route } from '../ui/route.ts'
import { prHash, parsePRHash, parseIssueHash, parseFileHash } from '../prs/prlink.ts'
import type { Member } from '../api/room.ts'

// The member whose transcript is open. The root conversation is one made from the seat.
export interface Drill extends Member {
  id: string
  rootSeat?: boolean
}

/** The page's own work that changing the screen needs. App binds these once. */
export interface RouterHost {
  /** Screens may show: boot is past and the first room is chosen. */
  started(): boolean
  /** A push deep link (#pop=room:session) owns the screen until it closes. */
  pop(): boolean
  /** Follow the address in the address bar; false when it names no room. */
  followHash(): boolean
  /** Go back to the all-rooms page. */
  leaveRoom(): void
  /** Close the member threads, the members and pins sheets, and the phone drawer. */
  closePanels(): void
  /** Close the phone drawer only. */
  closeDrawer(): void
  /** The view name this room shows for v. */
  resolveView(v: string): string
  /** Trim the feed when another view opens; a repeat tap keeps the reader's place. */
  trimFeedAtView(next: string, previous: string): void
  /** Drop the fields that belong to the screen the reader leaves. */
  leaveScreen(v: string, previous: string): void
  /** Forget the open run's detail when another run opens. */
  clearRunDetail(): void
  loadChanges(): void
  loadReviewer(): void
  loadRuns(): void
  loadRun(): void
}

let roomId = $state('')
let view = $state<string>(GENERAL)
let drill = $state<Drill | null>(null)
let sessionTurn = $state(0) // a completion link opens its exact stored turn
let openRun = $state('') // the run whose detail is open, in the runs view
let openJob = $state('') // the shell job whose detail is open, in the jobs view
let reviewOpen = $state(false)
let reviewTab = $state('files')
let reviewVersion = $state(0)
let readOnlyDm = $state(false) // the open transcript came from Services: no header tabs, no composer
let prLink = $state<ReturnType<typeof parsePRHash> | ReturnType<typeof parseIssueHash>>(null) // a PR link from the address bar, for the Pull requests view
let routing = $state(0) // links being followed
let routeFix = true // the next address write corrects the current entry
let detailRequest = $state(0) // the newest detail request; an older answer must not open

/** The screen state, written from App where a screen change starts there. */
export const router = {
  get roomId() { return roomId }, set roomId(v: string) { roomId = v },
  get view() { return view }, set view(v: string) { view = v },
  get drill() { return drill }, set drill(v: Drill | null) { drill = v },
  get sessionTurn() { return sessionTurn }, set sessionTurn(v: number) { sessionTurn = v },
  get openRun() { return openRun }, set openRun(v: string) { openRun = v },
  get openJob() { return openJob }, set openJob(v: string) { openJob = v },
  get reviewOpen() { return reviewOpen }, set reviewOpen(v: boolean) { reviewOpen = v },
  get reviewTab() { return reviewTab }, set reviewTab(v: string) { reviewTab = v },
  get reviewVersion() { return reviewVersion }, set reviewVersion(v: number) { reviewVersion = v },
  get readOnlyDm() { return readOnlyDm }, set readOnlyDm(v: boolean) { readOnlyDm = v },
  get prLink() { return prLink }, set prLink(v: ReturnType<typeof parsePRHash> | ReturnType<typeof parseIssueHash>) { prLink = v },
  get routing() { return routing }, set routing(v: number) { routing = v },
  get routeFix() { return routeFix }, set routeFix(v: boolean) { routeFix = v },
  get detailRequest() { return detailRequest }, set detailRequest(v: number) { detailRequest = v },
  syncRoute,
  hashChanged,
  routeHere,
  openView,
  openReview,
  closeReview,
  showRun,
  openPR,
  clearItemLink,
  holdsItemLink,
}

// App binds the page's side of navigation in its script, before the first
// effect runs, so no call reaches these first.
const missing = (name: string): never => { throw new Error(`router: ${name} is unbound`) }
let host: RouterHost = {
  started: () => missing('started'),
  pop: () => missing('pop'),
  followHash: () => missing('followHash'),
  leaveRoom: () => missing('leaveRoom'),
  closePanels: () => missing('closePanels'),
  closeDrawer: () => missing('closeDrawer'),
  resolveView: () => missing('resolveView'),
  trimFeedAtView: () => missing('trimFeedAtView'),
  leaveScreen: () => missing('leaveScreen'),
  clearRunDetail: () => missing('clearRunDetail'),
  loadChanges: () => missing('loadChanges'),
  loadReviewer: () => missing('loadReviewer'),
  loadRuns: () => missing('loadRuns'),
  loadRun: () => missing('loadRun'),
}

export function bindRouter(bound: RouterHost): void {
  host = bound
}

// syncRoute puts the open screen's address in the address bar
// (lib/ui/route.ts). A followed link passes through other screens while it
// loads, so routing holds the address until it lands. The first write, and
// the one after a followed link, correct the current entry instead of adding one.
function syncRoute() {
  // A service's read-only conversation keeps the Services address.
  const session = readOnlyDm ? '' : routeSession(drill)
  const hash = routeHash({ room: roomId, view, session, turn: session ? sessionTurn : 0, run: view === 'runs' ? openRun : '', review: reviewOpen ? reviewTab : undefined })
  if (!host.started() || routing || host.pop()) return
  // An item link is already the right entry; the next screen adds its own.
  if (!holdsItemLink()) writeRoute(hash, routeFix, location, history)
  routeFix = false
}

// Back or Forward to the all-rooms page leaves no address. The hash is read
// first: hashTarget drops a pairing code from it.
function hashChanged() {
  const empty = !location.hash
  if (!host.followHash() && empty && roomId) host.leaveRoom()
}

// routeHere follows an address in the open room without reloading the room.
// It returns false when the screen already shows the address.
function routeHere(r: Route): { roomId: string; request: number } | false {
  // A followed address always ends a detail navigation that is still loading:
  // its older response must not open over the screen the reader asked for.
  detailRequest++
  const sameChat = r.session ? routeSession(drill) === r.session && sessionTurn === r.turn : r.run ? !drill && view === 'runs' && openRun === r.run : !drill && !openRun && view === r.view
  if (sameChat && (r.review ?? '') === (reviewOpen && r.view !== 'issues' ? reviewTab : '')) return false
  reviewOpen = false
  if (!r.session && !r.run) openView(r.view)
  if (r.review) openReview(r.review)
  return { roomId, request: detailRequest }
}

function openReview(tab = reviewTab) {
  detailRequest++
  if (tab !== reviewTab) prLink = null
  reviewTab = tab
  reviewOpen = true
  host.closePanels()
  reviewVersion++
  if (!drill && tab === 'issues') view = 'issues'
  else if (!drill && !isFeedView(view)) view = GENERAL
  host.loadChanges()
  host.loadReviewer()
}

function closeReview() {
  reviewOpen = false
  prLink = null
  if (view === 'issues') view = GENERAL
  writeRoute(routeHash({ room: roomId, view, session: routeSession(drill), turn: sessionTurn, run: '' }), true, location, history)
}

function openView(v: string) {
  if (v === 'files' || v === 'github') { openReview(v === 'files' ? 'files' : 'prs'); return }
  reviewOpen = false
  clearItemLink(roomId, v)
  detailRequest++
  prLink = null
  drill = null
  openRun = ''
  openJob = ''
  host.leaveScreen(v, view)
  // A new view opens at the bottom, so older posts can go. Selecting the
  // open view again keeps the reader's place, so it trims only at the bottom.
  const next = host.resolveView(v)
  host.trimFeedAtView(next, view)
  view = next
  if (next === 'issues') openReview('issues')
  host.closeDrawer()
  if (v === 'runs') host.loadRuns()
}

function showRun(r: { id: string }) {
  clearItemLink()
  detailRequest++
  drill = null
  view = 'runs'
  if (openRun !== r.id) host.clearRunDetail()
  openRun = r.id
  host.loadRun()
}

function openPR(n: number | string) {
  history.pushState({ pr: true }, '', prHash(roomId, n, 'conversation', routeSession(drill)))
  openView('github')
  prLink = { roomId, pr: String(n), tab: 'conversation' }
}

// Leaving an item's room or view drops its link. The item's history entry
// stays, so Back reopens it; syncRoute gives the new screen its own entry.
function clearItemLink(nextRoom = '', nextView = '') {
  const issue = parseIssueHash(location.hash)
  const link = issue || parsePRHash(location.hash)
  if (link && (link.roomId !== nextRoom || nextView !== (issue ? 'issues' : 'github'))) prLink = null
}

// An open PR, issue or file keeps its own, more exact address.
function holdsItemLink() {
  if ((new URLSearchParams(location.hash.slice(1)).get('session') ?? '') !== routeSession(drill)) return false
  const issue = parseIssueHash(location.hash)
  const pr = parsePRHash(location.hash)
  const file = parseFileHash(location.hash)
  const link: [string, { roomId: string }] | null = issue ? ['issues', issue] : pr ? ['github', pr] : file ? ['files', file] : null
  return !!link && link[1].roomId === roomId && (link[0] === 'issues' ? view === 'issues' : reviewOpen && reviewTab === (link[0] === 'files' ? 'files' : 'prs'))
}
