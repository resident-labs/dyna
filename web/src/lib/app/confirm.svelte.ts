// The page's confirm dialog: the request it asks about, the words it shows for
// each kind, and the one request the answer sends. lib/ui/Confirm.svelte draws
// the dialog and lib/rooms/expedite.ts holds the expedite store; this module
// keeps the state and the wording, so App.svelte only hosts it.
import { api } from '../../api.ts'
import { errorText } from '../errorText.ts'
import { clipText } from '../ui/toast.ts'
import { router } from './router.svelte.ts'
import type { Goal } from '../api/goal.ts'
import type { Member } from '../api/room.ts'
import type { RunSummary } from '../api/workflow.ts'
import type { ToastInput } from '../ui/toast.ts'
import { allowsForceClose, closeRecoveryOf, type Responsibility } from '../api/session.ts'

/** What the page asks to confirm. */
export type Confirming =
  | { kind: 'promote' | 'stop' | 'close'; m: Member; busy?: boolean; error?: string }
  | { kind: 'forceclose'; m: Member; items: Responsibility[]; detail?: string; busy?: boolean; error?: string }
  | { kind: 'cancelrun'; run: Pick<RunSummary, 'id' | 'name' | 'status'>; busy?: boolean; error?: string }
  | { kind: 'expedite'; goal: Goal; roomId: string; busy?: boolean; error?: string }

/** The words of one confirm dialog, as lib/ui/Confirm.svelte draws them. */
export interface ConfirmText {
  title: string
  lines: string[]
  action: string
  danger?: boolean
  alternative?: string
  onalternative?: () => unknown
}

/** The page's own side of the confirm dialog. App binds these once. */
export interface ConfirmHost {
  /** The room's sessions, which the close text counts children from. */
  members(): Member[]
  /** The goal the room expedites now, which the replace text names. */
  expedited(): string
  /** The page shows the close summary the session returned. */
  closed(name: string, items: string[]): void
  loadGoals(): void
  loadRuns(): void
  loadRun(): void
  loadMembers(): void
  loadChanges(): void
  say(input: ToastInput): void
}

// App binds the page's side during its script, before the first effect runs.
const missing = (name: string): never => { throw new Error(`confirm: ${name} is unbound`) }
let confirmHost: ConfirmHost = {
  members: () => missing('members'),
  expedited: () => missing('expedited'),
  closed: () => missing('closed'),
  loadGoals: () => missing('loadGoals'),
  loadRuns: () => missing('loadRuns'),
  loadRun: () => missing('loadRun'),
  loadMembers: () => missing('loadMembers'),
  loadChanges: () => missing('loadChanges'),
  say: () => missing('say'),
}

export function bindConfirm(bound: ConfirmHost): void {
  confirmHost = bound
}

const roomId = $derived(router.roomId)

let confirm = $state<Confirming | null>(null)

// askConfirm opens the dialog for one request, as a menu item or a panel does.
function askConfirm(request: Confirming) {
  confirm = request
}

// dismiss closes the dialog without sending anything.
function dismiss() {
  confirm = null
}

function forceCloseLines(responsibilities: Responsibility[], detail?: string): string[] {
  const lines = detail ? [detail] : []
  lines.push(...responsibilities.map((item) => item.text))
  lines.push('Retry after fixing the error, or close anyway. The work above stays unresolved.')
  return lines
}

let confirmText = $derived.by((): ConfirmText | null => {
  if (!confirm) return null
  if (confirm.kind === 'promote') {
    return {
      title: `Promote ${confirm.m.name} to lead?`,
      lines: ['This session becomes a top-level lead. Its children stay attached, with updated depths.', 'Its active work and full conversation continue.'],
      action: 'Promote to lead', danger: false,
    }
  }
  if (confirm.kind === 'forceclose') {
    return {
      title: `Could not hand over all work from ${confirm.m.name}`,
      lines: forceCloseLines(confirm.items, confirm.detail),
      action: 'Close anyway',
    }
  }
  if (confirm.kind === 'expedite') {
    return {
      title: `Expedite goal ${confirm.goal.id}?`,
      lines: [`Goal ${confirmHost.expedited()} is expedited now. A room has one expedited goal, so this replaces it.`],
      action: 'Replace and expedite', danger: false,
    }
  }
  if (confirm.kind === 'cancelrun') {
    return {
      title: `Cancel ${confirm.run.name}?`,
      lines: ['Its running agents stop now. Work they finished stays in the run record.'],
      action: 'Cancel run',
    }
  }
  const m = confirm.m
  if (confirm.kind === 'stop') {
    return {
      title: `Stop the turn of ${m.name}?`,
      lines: ['The running turn stops now, and its unfinished work is lost.', 'The session stays open. Your next post to it starts a new turn.'],
      action: 'Stop turn',
    }
  }
  return closeConfirm(m, confirmHost.members().some((child) => !child.closed && child.parent === m.id))
})

// closeConfirm is the close dialog: the one action the server takes for a
// close, and what that close does with the session's work. It offers no goal
// handoff to the owner: root and the owner hold no goals, so a close refuses a
// goal that no open agent can take and names the commands that resolve it.
function closeConfirm(m: Member, hasChildren: boolean): ConfirmText {
  const lines = ['The session closes, and its name becomes free.', 'PR authorship moves to its nearest open ancestor, or root. Reviewer roles return to the pool. Live workflows are canceled.']
  lines.push('A held goal moves to its nearest open agent. Root and the owner hold no goals, so a goal with no such agent stops the close: hand it to an open session, or finish or drop it first.')
  if (m.state === 'live') lines.push('Its running turn stops first.')
  if (hasChildren) lines.push('Its open children that own no work close too. Children that still own work move to its nearest open ancestor and keep running.')
  return { title: `Close ${m.name}?`, lines, action: 'Close session' }
}

async function runConfirm() {
  const request = confirm
  if (!request) return
  const { kind } = request
  confirm = { ...request, busy: true, error: '' }
  if (kind === 'expedite') {
    try {
      await api.expediteGoal(request.roomId, request.goal.id, true)
      confirmHost.say({ tone: 'success', subject: 'Expedited the goal', text: clipText(request.goal.text) })
      confirm = null
      confirmHost.loadGoals()
    } catch (e) {
      confirm = { ...request, busy: false, error: errorText(e) }
    }
    return
  }
  if (kind === 'cancelrun') {
    try {
      await api.cancelRun(request.run.id)
      confirmHost.say({ tone: 'success', subject: `${request.run.name} is canceled`, text: 'Work it finished stays in the run record.' })
      confirm = null
      confirmHost.loadRuns()
      confirmHost.loadRun()
    } catch (e) {
      confirm = { ...request, busy: false, error: errorText(e) }
    }
    return
  }
  const m = request.m
  if (!m.id) return
  try {
    if (kind === 'stop') await api.stopSession(roomId, m.id)
    else if (kind === 'promote') await api.promoteSession(roomId, m.id)
    else {
      const result = await api.closeSession(roomId, m.id, kind === 'forceclose')
      confirmHost.closed(m.name, result?.closeSummary ?? [])
    }
    confirm = null
    if (kind === 'stop') confirmHost.say({ tone: 'success', subject: 'Turn stopped', text: `${m.name} stays open; your next post starts a new turn.` })
    else if (kind === 'promote') confirmHost.say({ tone: 'success', subject: `${m.name} is now a top-level lead`, text: 'Its children stay attached, with new depths.' })
    else confirmHost.say({ tone: 'success', subject: `${m.name} is closed`, text: 'Its PR authorship and reviewer roles have moved on.' })
    confirmHost.loadMembers()
    confirmHost.loadChanges()
  } catch (e) {
    const msg = errorText(e)
    const recovery = closeRecoveryOf(e)
    // Force is offered only when the server allows that recovery.
    if (kind === 'close' && recovery && allowsForceClose(e)) confirm = { kind: 'forceclose', m, items: recovery.items, detail: msg }
    else confirm = { ...request, busy: false, error: msg }
  }
}

/** The confirm dialog, named as the page named it when it lived there. */
export const confirmView = {
  get confirm() { return confirm },
  get confirmText() { return confirmText },
  askConfirm,
  dismiss,
  runConfirm,
}
