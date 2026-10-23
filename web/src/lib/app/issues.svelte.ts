// The room's issues: the open ones the Issues tab lists and the closed or
// converted ones it keeps below them. lib/prs/GitHub.svelte draws them;
// lib/prs/unifiedIssues holds the row shape. This module keeps the lists and
// the requests that keep them current, so App.svelte only hosts them.
import { api } from '../../api.ts'
import { errorText } from '../errorText.ts'
import { reconcileRows } from '../ui/reloadGate.ts'
import { router } from './router.svelte.ts'
import type { RoomIssue } from '../prs/unifiedIssues.ts'
import type { ToastInput } from '../ui/toast.ts'

/** The prompt a close reason asks for; Prompt.svelte draws it. */
export interface CloseIssuePrompt {
  title: string
  lines?: string[]
  label?: string
  action?: string
  submit: (value: string) => void | Promise<unknown>
}

/** The page's own side of the issues. App binds these once. */
export interface IssuesHost {
  /** The page's reload gate, so a list keeps one request and one refresh. */
  reload(name: string, load: (current: () => boolean) => void | Promise<unknown>): Promise<void>
  /** Show a loader failure. */
  error(message: string): void
  /** The Issues view takes the panels' place. */
  openIssuesView(): void
  /** Ask for the close reason. */
  ask(prompt: CloseIssuePrompt): void
  /** Close the open prompt. */
  clearAsk(): void
  /** Say one line about a finished action. */
  say(input: ToastInput): void
}

// App binds the page's side during its script, before the first effect runs.
const missing = (name: string): never => { throw new Error(`issues: ${name} is unbound`) }
let issuesHost: IssuesHost = {
  reload: () => missing('reload'),
  error: () => missing('error'),
  openIssuesView: () => missing('openIssuesView'),
  ask: () => missing('ask'),
  clearAsk: () => missing('clearAsk'),
  say: () => missing('say'),
}

export function bindIssues(bound: IssuesHost): void {
  issuesHost = bound
}

const roomId = $derived(router.roomId)

let issues = $state<RoomIssue[]>([]) // open issues, newest first
let issueHistory = $state<RoomIssue[]>([]) // closed and converted issues, newest first

// loadIssues reads one of the two lists. A slow answer for an older room never
// replaces a newer one.
function loadIssues(history = false) {
  return issuesHost.reload(history ? 'issueHistory' : 'issues', async (current: () => boolean) => {
    if (!roomId) return
    const id = roomId
    try {
      const list = (await api.issues(id, history)) ?? []
      if (!current() || id !== roomId) return
      if (history) issueHistory = reconcileRows(issueHistory, list.filter((i: RoomIssue) => i.state !== 'open'))
      else issues = reconcileRows(issues, list)
    } catch (e) {
      if (current()) issuesHost.error(String(e))
    }
  })
}

// Notices open the same Issues tab as the room navigation.
function openIssues() { issuesHost.openIssuesView() }

// One issue action: run the call, then refresh both lists.
async function triageIssue(call: () => Promise<unknown>) {
  try {
    await call()
    issuesHost.error('')
    loadIssues()
    loadIssues(true)
  } catch (e) {
    issuesHost.say({ tone: 'error', subject: 'Could not update the issue', text: errorText(e) })
  }
}

// Close needs a reason: the backend rejects an empty one.
function askCloseIssue(i: RoomIssue) {
  issuesHost.ask({
    title: 'Close issue',
    label: 'Reason',
    lines: [i.title],
    action: 'Close',
    submit: async (reason) => {
      issuesHost.clearAsk()
      await triageIssue(() => api.closeIssue(roomId, i.id, reason))
    },
  })
}

/** The room's issues, named as the page named them when they lived there. */
export const issueView = {
  get issues() { return issues },
  get issueHistory() { return issueHistory },
  loadIssues,
  openIssues,
  triageIssue,
  askCloseIssue,
  /** A room pick drops the old room's issues. */
  clear() {
    issues = []
    issueHistory = []
  },
}
