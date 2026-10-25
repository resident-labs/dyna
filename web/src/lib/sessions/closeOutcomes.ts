import type { CloseFailure, CloseOutcome, CloseOutcomeResult } from '../api/room.ts'

/** CloseTarget is the work a close row can open. */
export type CloseTarget =
  | { kind: 'goal'; id: string }
  | { kind: 'pr'; number: number }
  | { kind: 'session'; name: string }

/** CloseChoice is the control a failed row offers: the one step that resolves it. */
export type CloseChoice = 'move-goal' | 'move-child' | 'open-pr'

/** CloseRow is one drawn line of a session close result. */
export interface CloseRow {
  key: string
  /** What the work was, such as 'PR #42' or 'Goal g7'. */
  subject: string
  /** What the close did with it, and where it went when it moved. */
  result: string
  /** Why a step did not run. */
  failure?: string
  /** The work this row opens, when the app can show it. */
  target?: CloseTarget
  /** The control that resolves a failed row. */
  choice?: { action: CloseChoice; label: string }
  failed: boolean
}

// The app words every row from the typed facts, so a change of the engine's own
// wording never reaches this view.

const RESULT_TEXT: Record<CloseOutcomeResult, string> = {
  moved: 'handed over',
  closed: 'closed',
  canceled: 'canceled',
  withdrawn: 'withdrawn',
  returned: 'returned to its goal',
  stopped: 'stops unless kept',
  released: 'reviewer role released',
  deferred: 'reviewer release waits for the saved verdict',
  failed: 'did not finish',
}

const FAILURE_TEXT: Record<CloseFailure, string> = {
  handoff: 'The work did not move.',
  'goal-needs-agent': 'No open agent can hold the goal.',
  'child-heir': 'The children found no open session above them.',
  'child-handoff': 'The child stays open under the closed session.',
  'process-cleanup': 'The session process did not stop.',
}

// CHOICES words the one control each failure allows. The engine names the
// failure; only the app decides what the owner can press.
const CHOICES: Partial<Record<CloseFailure, { action: CloseChoice; label: string }>> = {
  'goal-needs-agent': { action: 'move-goal', label: 'Hand off the goal' },
  'child-handoff': { action: 'move-child', label: 'Move the child' },
  'child-heir': { action: 'move-child', label: 'Open a child' },
  handoff: { action: 'open-pr', label: 'Open the PR' },
}

/** closeRows draws the typed facts of a close as rows. */
export function closeRows(outcomes: CloseOutcome[]): CloseRow[] {
  return outcomes.map((outcome, index) => {
    const failed = outcome.result === 'failed'
    const failure = failed ? outcome.failure : undefined
    return {
      key: `${outcome.kind ?? 'close'}:${outcome.id ?? outcome.number ?? outcome.name ?? index}`,
      subject: subjectOf(outcome),
      result: resultOf(outcome),
      failure: failure ? FAILURE_TEXT[failure] : undefined,
      target: targetOf(outcome),
      choice: failure ? choiceOf(failure, outcome) : undefined,
      failed,
    }
  })
}

// closeSummaryText is one line for a list or a bulk row.
export function closeSummaryText(outcomes: CloseOutcome[]): string {
  return closeRows(outcomes)
    .map((row) => [row.subject, row.result].join(' '))
    .join('; ')
}

function resultOf(outcome: CloseOutcome): string {
  // Work that moved says who took it, and says when it became top level.
  if (outcome.result === 'moved') return `handed over to ${outcome.to || 'top level'}`
  return RESULT_TEXT[outcome.result]
}

function subjectOf(outcome: CloseOutcome): string {
  switch (outcome.kind) {
    case 'pr':
      return `PR #${outcome.number ?? 0}`
    case 'goal':
      return `Goal ${outcome.id ?? ''}`.trim()
    case 'item':
      return `Goal item ${outcome.id ?? ''}`.trim()
    case 'question':
      return `Question ${outcome.id ?? ''}`.trim()
    case 'child':
      return outcome.name ?? 'Child session'
    case 'shell':
      return `Shell ${outcome.id ?? ''}`.trim()
    case 'workflow':
      return `Workflow ${outcome.id ?? ''}`.trim()
  }
  // No kind means the step concerned the close itself, not one kind of work.
  return 'Session close'
}

function targetOf(outcome: CloseOutcome): CloseTarget | undefined {
  switch (outcome.kind) {
    case 'pr':
      return outcome.number ? { kind: 'pr', number: outcome.number } : undefined
    case 'goal':
      return outcome.id ? { kind: 'goal', id: outcome.id } : undefined
    case 'child':
      return outcome.name ? { kind: 'session', name: outcome.name } : undefined
  }
  return undefined
}

function choiceOf(failure: CloseFailure, outcome: CloseOutcome): { action: CloseChoice; label: string } | undefined {
  const choice = CHOICES[failure]
  if (!choice) return undefined
  // A choice needs a place to act; a row without one offers no control.
  if (choice.action === 'move-goal' && !outcome.id) return undefined
  if (choice.action === 'move-child' && !outcome.name) return undefined
  if (choice.action === 'open-pr' && !outcome.number) return undefined
  return choice
}
