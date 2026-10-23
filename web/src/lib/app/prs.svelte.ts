// The room's pull requests: the Changes rows the PR screen and the agent chips
// read, the room's reviewer, and the tracking actions those rows offer.
// lib/prs holds the row shapes and the screen; this module keeps the rows and
// the requests that keep them current, so App.svelte only hosts them.
import { api } from '../../api.ts'
import { errorText } from '../errorText.ts'
import { prsBySession, isOpen } from '../prs/pr.ts'
import { router } from './router.svelte.ts'
import type { Member } from '../api/room.ts'
import type { ReviewerStatus } from '../api/change.ts'
import type { Row as ChangeRow } from '../prs/prlist.ts'
import type { ToastInput } from '../ui/toast.ts'

/** A page of Changes rows, with its continuation cursor. */
interface ChangesPage {
  items: ChangeRow[] | null
  older?: boolean
  cursor?: string
}

/** The page's own side of the pull requests. App binds these once. */
export interface PRsHost {
  /** The page's reload gate, so a list keeps one request and one refresh. */
  reload(name: string, load: (current: () => boolean) => void | Promise<unknown>): Promise<void>
  /** A Changes page joins the list, and the rows it now holds are returned. */
  applyPage(page: ChangesPage): ChangeRow[]
  /** The page is still the one the reader chose. */
  alive(): boolean
  /** Say one line about a finished action. */
  say(input: ToastInput): void
}

// App binds the page's side during its script, before the first effect runs.
const missing = (name: string): never => { throw new Error(`prs: ${name} is unbound`) }
let prsHost: PRsHost = {
  reload: () => missing('reload'),
  applyPage: () => missing('applyPage'),
  alive: () => missing('alive'),
  say: () => missing('say'),
}

export function bindPRs(bound: PRsHost): void {
  prsHost = bound
}

const roomId = $derived(router.roomId)

let reviewer = $state<ReviewerStatus | null>(null) // the room's reviewer (ReviewerStatus)
let changes = $state<ChangeRow[] | null>(null) // the room's changes, local and PR (Changes); null before they load or on a host without the API
let mergeVersion = $state(0)
let prError = $state('')
// The room's pull requests: the Changes rows that are not local changes.
let prs = $derived((changes ?? []).filter((c) => c.kind !== 'local'))
let prMap = $derived(prsBySession(prs))
// Agent rows show a chip only while a PR is open; the panel lists them all.
let openPRMap = $derived(prsBySession(prs.filter(isOpen)))

function loadReviewer() {
  return prsHost.reload('reviewer', async (current: () => boolean) => {
    const id = roomId
    if (!id) return
    const status = await api.reviewerStatus(id).catch(() => null)
    if (current() && prsHost.alive() && id === roomId) reviewer = status
  })
}

// A burst of PR events refreshes the reviewer once.
let reviewerTimer: ReturnType<typeof setTimeout> | undefined
function reviewerSoon() {
  clearTimeout(reviewerTimer)
  reviewerTimer = setTimeout(loadReviewer, 250)
}

function loadChanges() {
  return prsHost.reload('changes', async (current: () => boolean) => {
    const id = roomId
    try {
      const all = await api.changes(id)
      if (!current()) return
      changes = prsHost.applyPage(all)
      prError = ''
    } catch (e) {
      if (current()) prError = errorText(e)
    }
  })
}

async function untrackPR(pr: ChangeRow) {
  if (!pr.key) return
  try {
    await api.untrackPR(roomId, pr.key)
    prsHost.say({ tone: 'success', subject: `Dyna stopped watching #${pr.number}`, text: 'Its updates no longer arrive here.' })
  } catch (e) {
    prsHost.say({ tone: 'error', subject: 'Could not stop watching that pull request', text: errorText(e) })
  }
  loadChanges()
}

// trackPR links a PR to a session from the pull-requests view.
async function trackPR(number: number | string | undefined, m: Member) {
  if (number === undefined || !m.id) return
  try {
    await api.trackPR(roomId, m.id, Number(number))
    prsHost.say({ tone: 'success', subject: `Dyna watches #${number}`, text: `Review and CI updates go to ${m.name}.` })
  } catch (e) {
    prsHost.say({ tone: 'error', subject: 'Could not watch that pull request', text: errorText(e) })
  }
  loadChanges()
}

async function resumePR(pr: ChangeRow) {
  if (!pr.key) return
  try {
    await api.resumePR(roomId, pr.key)
    prsHost.say({ tone: 'success', subject: `Dyna watches #${pr.number} again`, text: 'Review and CI updates resume.' })
  } catch (e) {
    prsHost.say({ tone: 'error', subject: 'Could not resume watching that pull request', text: errorText(e) })
  }
  loadChanges()
}

/** The room's pull requests, named as the page named them when they lived there. */
export const changeView = {
  get changes() { return changes },
  get reviewer() { return reviewer },
  get mergeVersion() { return mergeVersion },
  get prs() { return prs },
  get error() { return prError },
  get bySession() { return prMap },
  get openBySession() { return openPRMap },
  loadReviewer,
  reviewerSoon,
  loadChanges,
  untrackPR,
  trackPR,
  resumePR,
  /** A prs or changes event moves the screen's rows, so the PR screen redraws. */
  bumpVersion() { mergeVersion += 1 },
  /** An older Changes page joins the list. */
  setChanges(rows: ChangeRow[]) { changes = rows },
  /** A room pick drops the old room's rows. */
  clear() {
    changes = null
    reviewer = null
  },
}
