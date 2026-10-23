// The open session's transcript: its pages, the steering queue and the thread
// count the header shows. lib/sessions/sessionData.ts decides when to read and
// merges nothing; lib/sessions/sessionPages.ts joins a page to the window and
// lib/sessions/dm.ts counts the threads. This module keeps the state the screen
// shows, so App.svelte only hosts it.
import { api } from '../../api.ts'
import { createSessionData } from '../sessions/sessionData.ts'
import { mergeSessionPage } from '../sessions/sessionPages.ts'
import { dmView } from '../sessions/dm.ts'
import { roomData } from './roomLoader.svelte.ts'
import { router } from './router.svelte.ts'
import type { QueueItem, Session } from '../api/session.ts'
import type { Receipt } from '../sessions/dm.ts'
import type { ToastInput } from '../ui/toast.ts'

export type { QueueItem, Receipt, Session }

/** The page's own side of the transcript. App binds these once. */
export interface SessionViewHost {
  /** The window shows the room at all, next to a phone's review pane. */
  pageOpen(): boolean
  /** The open session tab: chat, computer or media. */
  tab(): string
  /** Say one line about a finished action. */
  say(input: ToastInput): void
}

// App binds the page's side during its script, before the first effect runs.
const missing = (name: string): never => { throw new Error(`sessionView: ${name} is unbound`) }
let sessionHost: SessionViewHost = {
  pageOpen: () => missing('pageOpen'),
  tab: () => missing('tab'),
  say: () => missing('say'),
}

export function bindSessionView(bound: SessionViewHost): void {
  sessionHost = bound
}

const roomId = $derived(router.roomId)
const room = $derived(roomData.feed?.room ?? null)

let session = $state<Session | null>(null)
let sessionReadingHistory = $state(false)
let sessionVisit = 0
let queue = $state<QueueItem[]>([]) // steering that waits for the open session's turn
let sessionError = $state('')
let threadCount = $derived(dmView(session, room || { name: '' }).threads.length)

// sessionVisible tells whether a transcript tab is on screen, so a hidden one
// neither reads nor polls.
function sessionVisible(tab = sessionHost.tab()) {
  return sessionHost.pageOpen() && ['chat', 'computer', 'media'].includes(tab)
}

// showSession joins a page to the open window.
function showSession(value: Session, older: boolean | string = false) {
  session = mergeSessionPage(session, value, older, sessionReadingHistory)
  sessionError = ''
  // The root seat opened before root had a session of its own. Once RootSession returns the real
  // session (root started, or the seat was released), continue as that member: its queue, paging and events.
  if (router.drill?.rootSeat && value.member?.id) {
    router.drill = { ...value.member, id: value.member.id }
    loadSession()
  }
}

const data = createSessionData(api, {
  current: () => ({ room: roomId, visit: roomData.roomRequest, id: router.drill?.id ?? '', root: !!router.drill?.rootSeat, selection: String(sessionVisit), turn: router.sessionTurn, visible: sessionVisible(), live: session?.member?.state === 'live' }),
  transcript: showSession,
  queue: (value) => (queue = value),
  error: (error) => (sessionError = error),
})

function loadSession() { return data.load() }

function loadEarlierSession(near = () => true) {
  if (!session?.earlier || router.sessionTurn) return Promise.resolve(false)
  return data.earlier(session.cursor ?? '', near)
}

function loadNewerSession(near = () => true) {
  if (!session?.newer || router.sessionTurn) return Promise.resolve(false)
  return data.newer(String(session.turns?.at(-1)?.n ?? ''), near)
}
function presentSession() { return data.present() }

async function unqueue(q: Receipt) {
  if (!router.drill) return
  await api.deletePendingSteer(router.drill.id, q.id)
  queue = queue.filter((x) => x.id !== q.id)
  sessionHost.say({ tone: 'success', subject: 'Removed the queued message', text: 'The turn will not see it.' })
}

// setSession takes a transcript that arrived with the session, so it also
// clears the error of a read that failed before it.
function setSession(value: Session | null) {
  session = value
  sessionError = ''
}

async function dismissPendingPost(id: string, messageId: string) {
  await api.dismissPendingPost(id, messageId, true)
  if (router.drill?.id === id) queue = queue.filter((post) => post.id !== messageId)
  sessionHost.say({ tone: 'success', subject: 'Message dismissed', text: 'Its history stays available.' })
}

/** The open session's transcript, named as the page named it when it lived there. */
export const sessionView = {
  get session() { return session },
  get queue() { return queue },
  get error() { return sessionError },
  get threadCount() { return threadCount },
  get readingHistory() { return sessionReadingHistory },
  set readingHistory(value: boolean) { sessionReadingHistory = value },
  get visit() { return sessionVisit },
  /** Opening a session starts a new visit, so an older answer cannot land. */
  nextVisit() { sessionVisit++ },
  /** Another session opens: its transcript, queue and error start empty. */
  openFresh() {
    session = null
    sessionError = ''
    queue = []
  },
  /** A room pick drops the old room's transcript. */
  clear() { session = null },
  /** A transcript that arrived with the session needs no read. */
  setSession,
  /** A member that changed, as a reopen or a dismiss does, keeps its transcript. */
  patchMember(member: Session['member']) {
    if (session && session.member?.id === member?.id) session = { ...session, member }
  },
  sessionVisible,
  showSession,
  /** Post events reach the transcript through the controller's own timer. */
  schedule() { data.schedule() },
  /** The app started: the open transcript may poll. */
  start() { data.start() },
  destroy() { data.destroy() },
  loadSession,
  loadEarlierSession,
  loadNewerSession,
  presentSession,
  unqueue,
  dismissPendingPost,
}
