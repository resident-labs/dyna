// Typed readers for the payload of a sealed system event. They read the
// stored payload only, never the post text, so a card and its event cannot
// disagree. The types come from events.gen.ts, which the Go registry writes.
import { eventSpecs, isAlertEvent, isCallEvent, isCheckinEvent, isEngineEvent, isGoalEvent, isHookEvent, isIssueEvent, isMachineEvent, isMergeDecisionEvent, isMergeEvent, isPREvent, isQuestionEvent, isReviewEvent, isRuleEvent, isRunEvent, isServiceFailedEvent, isSessionEvent, isShellEvent, isSlowTestEvent, isTurnEvent, isWorktreeEvent } from './events.gen.ts'
import type { AlertEvent, CallEvent, CheckinEvent, EngineEvent, EventPayload, GoalEvent, HookEvent, IssueEvent, MachineEvent, MergeDecisionEvent, MergeEvent, PREvent, QuestionEvent, ReviewEvent, RuleEvent, RunEvent, ServiceFailedEvent, SessionEvent, ShellEvent, SlowTestEvent, TurnEvent, WorktreeEvent } from './events.gen.ts'

// A post of one event family, with the fields its reader needs. A feed row, a
// stored message and a test fixture all fit it.
export interface EventPost {
  kind?: string
  from?: string
  notice?: { kind: string } | null
  payload?: EventPayload
}

// registeredEvent reports a post whose family and type the registry knows.
export function registeredEvent(post: EventPost): boolean {
  return eventSpecs.some((spec) => spec.family === post.kind && spec.type === post.notice?.kind)
}

// worktreeEvent returns the typed payload of a kept-worktree post.
export function worktreeEvent(post: EventPost): WorktreeEvent | undefined {
  if (post.kind !== 'worktree' || post.notice?.kind !== 'kept') return undefined
  return isWorktreeEvent(post.payload) ? post.payload : undefined
}

// hookEvent returns the typed payload of a hook run post, started or result.
export function hookEvent(post: EventPost): HookEvent | undefined {
  if (post.kind !== 'hook' || (post.notice?.kind !== 'started' && post.notice?.kind !== 'result')) return undefined
  return isHookEvent(post.payload) ? post.payload : undefined
}

// prEvent returns the typed payload of a PR watcher post. A post the watcher
// stored before the typed events existed has no payload, and returns
// undefined; the PR card then reads its stored text (pr.ts).
export function prEvent(post: EventPost): PREvent | undefined {
  if (post.kind !== 'pr') return undefined
  return isPREvent(post.payload) ? post.payload : undefined
}

// mergeEvent returns the typed payload of a merge trial post: its progress or
// how it ended. A recorded decision has its own payload.
export function mergeEvent(post: EventPost): MergeEvent | undefined {
  if (post.kind !== 'merge' || post.notice?.kind === 'decision') return undefined
  return isMergeEvent(post.payload) ? post.payload : undefined
}

// mergeDecision returns the typed payload of a recorded merge decision, which
// has no trial and its own card.
export function mergeDecision(post: EventPost): MergeDecisionEvent | undefined {
  if (post.kind !== 'merge' || post.notice?.kind !== 'decision') return undefined
  return isMergeDecisionEvent(post.payload) ? post.payload : undefined
}

// shellEvent returns the typed payload of a detached shell's result.
export function shellEvent(post: EventPost): ShellEvent | undefined {
  if (post.kind !== 'shell' || post.notice?.kind !== 'result') return undefined
  return isShellEvent(post.payload) ? post.payload : undefined
}

// runEvent returns the typed payload of a workflow run's result.
export function runEvent(post: EventPost): RunEvent | undefined {
  if (post.kind !== 'run' || post.notice?.kind !== 'result') return undefined
  return isRunEvent(post.payload) ? post.payload : undefined
}

// turnEvent returns the typed payload of the end of a turn, in any state.
export function turnEvent(post: EventPost): TurnEvent | undefined {
  if (post.kind !== 'turn') return undefined
  return isTurnEvent(post.payload) ? post.payload : undefined
}

// legacyEvent reports a post that names no event type but is still an engine
// event, so it draws its family's card instead of a message bubble.
// TODO: legacy/shim remove later — the scheduler posted check-ins from
// "system:parent-checkin" before the typed events; remove when that history
// ages out.
export function legacyEvent(post: EventPost): boolean {
  return post.kind === 'checkin' && post.from === 'system:parent-checkin'
}

// issueEvent returns the typed payload of an issue post. A post stored before
// the typed events has no payload, and returns undefined; the issue card then
// reads its stored text.
export function issueEvent(post: EventPost): IssueEvent | undefined {
  if (post.kind !== 'issue') return undefined
  return isIssueEvent(post.payload) ? post.payload : undefined
}

// goalEvent returns the typed payload of a goal post. A post stored before the
// typed events has no payload, and returns undefined; the goal row then reads
// its stored text.
export function goalEvent(post: EventPost): GoalEvent | undefined {
  if (post.kind !== 'goal') return undefined
  return isGoalEvent(post.payload) ? post.payload : undefined
}

// checkinEvent returns the typed payload of a parent check-in.
export function checkinEvent(post: EventPost): CheckinEvent | undefined {
  if (post.kind !== 'checkin') return undefined
  return isCheckinEvent(post.payload) ? post.payload : undefined
}

// slowTestEvent returns the typed payload of a CI health line about slow tests.
export function slowTestEvent(post: EventPost): SlowTestEvent | undefined {
  if (post.kind !== 'ci-health') return undefined
  return isSlowTestEvent(post.payload) ? post.payload : undefined
}

// ruleEvent returns the typed payload of a rule change.
export function ruleEvent(post: EventPost): RuleEvent | undefined {
  if (post.kind !== 'rule') return undefined
  return isRuleEvent(post.payload) ? post.payload : undefined
}

// reviewEvent returns the typed payload of a reviewer result post.
export function reviewEvent(post: EventPost): ReviewEvent | undefined {
  if (post.kind !== 'review') return undefined
  return isReviewEvent(post.payload) ? post.payload : undefined
}

// machineEvent returns the typed payload of a machine event: the sizes its probe
// read and the floor it missed.
export function machineEvent(post: EventPost): MachineEvent | undefined {
  if (post.kind !== 'machine' || post.notice?.kind !== 'low-disk') return undefined
  return isMachineEvent(post.payload) ? post.payload : undefined
}

// callEvent returns the typed payload of the owner's answer to a call
// request.
export function callEvent(post: EventPost): CallEvent | undefined {
  if (post.kind !== 'call') return undefined
  return isCallEvent(post.payload) ? post.payload : undefined
}

// sessionEvent returns the typed payload of a session note: a rename, a move,
// an account change, an instruction change, a root-seat change, a spawn or a
// handoff request for a model transfer.
export function sessionEvent(post: EventPost): SessionEvent | undefined {
  if (post.kind !== 'session') return undefined
  return isSessionEvent(post.payload) ? post.payload : undefined
}

// alertEvent returns the typed payload of an alert from the doctor or the
// engine.
export function alertEvent(post: EventPost): AlertEvent | undefined {
  if (post.kind !== 'alert') return undefined
  return isAlertEvent(post.payload) ? post.payload : undefined
}

// serviceFailedEvent returns the typed payload of a failed service turn.
export function serviceFailedEvent(post: EventPost): ServiceFailedEvent | undefined {
  if (post.kind !== 'service' || post.notice?.kind !== 'failed') return undefined
  return isServiceFailedEvent(post.payload) ? post.payload : undefined
}

// engineEvent returns the typed payload of a note from the engine itself.
export function engineEvent(post: EventPost): EngineEvent | undefined {
  if (post.kind !== 'engine') return undefined
  return isEngineEvent(post.payload) ? post.payload : undefined
}

// questionEvent returns the typed payload of a note about a question.
export function questionEvent(post: EventPost): QuestionEvent | undefined {
  if (post.kind !== 'question') return undefined
  return isQuestionEvent(post.payload) ? post.payload : undefined
}

// isQuestionWithdrawn reports the quiet note that a question was withdrawn.
export function isQuestionWithdrawn(post: EventPost): boolean {
  return post.kind === 'question' && post.notice?.kind === 'withdrawn'
}
