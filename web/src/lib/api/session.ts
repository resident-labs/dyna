// A session's transcript and its queued, transferred and consolidated work.
// Sources: internal/roomsvc/session.go, internal/subagent/queue.go,
// transfer.go, consolidation.go and internal/roomsvc/bulk.go.
import type { Member, Post } from './room.ts'
import type { ShellJob } from './terminal.ts'

// Usage is workeroutput.Usage.
export interface Usage {
  inputTokens?: number
  cachedTokens?: number
  outputTokens?: number
  costUsd?: number
}

// Item is roomsvc.Item, one transcript entry. kind is prompt, steering,
// reply, tool, thought, error, note or compaction. ts is an ISO time.
// StepKind is what a tool call did, in the session view's own words. The view
// draws a designed row per kind, never the harness's tool ID.
export type StepKind = 'read' | 'edit' | 'write' | 'search' | 'list' | 'run' | 'fetch' | 'delegate' | 'plan' | 'monitor' | 'tool'

export interface Item {
  postId?: string
  delivered?: boolean
  kind: string
  ts: string
  text?: string
  from?: string
  // step, label, toolName and durationMs describe a tool call: what it did,
  // the short name a row shows, the harness's own tool ID, and how long it
  // took. The ID and the full target stay in the row's details.
  step?: StepKind
  label?: string
  toolName?: string
  durationMs?: number
  tool?: string
  target?: string
  aside?: string
  failed?: boolean
  done?: boolean
  command?: string
  output?: string
  exit?: number | null
  jobId?: string
  jobOp?: string
  job?: ShellJob
  state?: string
  trigger?: string
  tokensBefore?: number
  tokensAfter?: number
  reason?: string
}

// Turn is roomsvc.Turn, one transcript turn.
export interface Turn {
  harness?: string
  outputDialect?: string
  profile?: string
  model?: string
  harnessSession?: string
  wrapUp?: boolean
  n: number
  status: string
  started: string
  ended: string
  usage: Usage
  items: Item[] | null
  omitted?: boolean
  // wake is why the turn started. The session view draws line; it does not parse it.
  wake?: { kind: string; postId?: string; sender?: string; name?: string; line: string }
}

// SessionReference is roomsvc.ReferenceRow: one post of another conversation,
// as a session's DM shows it. The row names the post and its home, so the
// reader jumps to the post where it lives instead of drawing a copy. ts is
// unix milliseconds.
export interface SessionReference extends Pick<Post, 'kind' | 'notice' | 'payload'> {
  postId: string
  from: string
  speaker: string
  fromHue: number
  // conversation is the home of the post, such as "room", "voice" or a DM.
  conversation: string
  ts: number
  preview: string
  attachments: number
}

// Session is roomsvc.Session: a member's drill-down.
export interface Session {
  cursor?: string
  earlier: boolean
  newer: boolean
  member: Member
  address: string
  posts: Post[] | null
  // references are the posts of other conversations that this one received
  // or wrote, in the same time window as posts.
  references?: SessionReference[] | null
  media: Post[] | null
  dir: string
  usage: Usage
  turns: Turn[] | null
}

// MemberProfile is roomsvc.Profile: a member's card. parent is the parent's name,
// empty for a top-level session; posts are the newest ones, oldest first.
export interface MemberProfile {
  member: Member
  parent?: string
  dir: string
  usage: Usage
  posts: Post[] | null
}

// QueueState is subagent.QueueState.
export type QueueState = 'waiting' | 'delivering' | 'delivered' | 'cancelled' | 'dismissed'

// QueueOrigin is subagent.QueueOrigin.
export interface QueueOrigin {
  session: string
  address: string
  state: QueueState
  targetDelivered?: boolean
}

// QueueItem is subagent.QueueItem. ts is unix milliseconds.
export interface QueueItem {
  human?: boolean
  origins?: QueueOrigin[]
  id: string
  text: string
  from: string
  ts: number
  turn: number
  state: QueueState
  cancellable: boolean
}

// TransferState is subagent.TransferState, a model transfer of one session.
export interface TransferState {
  rootSeat?: boolean
  pausedFor?: number
  waitFor?: number
  id: string
  session: string
  phase: string // waiting, stopping, starting, completed, failed, cancelled
  profile: string
  model?: string
  previousProfile: string
  previousModel?: string
  previousHarness?: string
  previousDir: string
  previousResumable: boolean
  oldTurns: number
  requested: string
  deadline: string
  startupDeadline?: string
  ended?: string
  turn?: number
  postId: string
  posted?: boolean
  writtenHandoff?: string
  handoff?: string
  dir?: string
  branch?: string
  source?: string
  convertedSession?: string
  reason?: string
  error?: string
}

// ConsolidationSource is subagent.ConsolidationSource.
export interface ConsolidationSource {
  pendingIds?: string[]
  reservation?: TransferState
  session: string
  address: string
  phase: string
  source?: string
  error?: string
  context?: string
  dir?: string
  cursor: number
}

// ConsolidationState is subagent.ConsolidationState: several sessions merged into one.
export interface ConsolidationState {
  pausedFor?: number
  id: string
  roomId: string
  target: string
  sessions: string[]
  profile: string
  model: string
  phase: string
  requested: string
  deadline: string
  ended?: string
  error?: string
  cancelPending?: boolean
  sources: ConsolidationSource[] | null
  charter?: string
}

export type CloseReason = 'responsibilities' | 'goal-needs-agent'
export type CloseAction = 'retry' | 'force-close'

export type ResponsibilityKind = 'pr' | 'goal' | 'item' | 'question' | 'child' | 'shell' | 'workflow'

// Responsibility is sessionclose.Responsibility from the engine.
export interface Responsibility {
  kind: ResponsibilityKind
  id: string
  text: string
}

// CloseRecovery is sessionclose.Recovery from the engine.
export interface CloseRecovery {
  reason: CloseReason
  actions: CloseAction[]
  items: Responsibility[]
}

const isCloseReason = (value: unknown): value is CloseReason => value === 'responsibilities' || value === 'goal-needs-agent'
const isCloseAction = (value: unknown): value is CloseAction => value === 'retry' || value === 'force-close'
const isResponsibilityKind = (value: unknown): value is ResponsibilityKind => value === 'pr' || value === 'goal' || value === 'item' || value === 'question' || value === 'child' || value === 'shell' || value === 'workflow'
const isResponsibility = (value: unknown): value is Responsibility => {
  if (typeof value !== 'object' || value === null) return false
  return isResponsibilityKind(Reflect.get(value, 'kind')) && typeof Reflect.get(value, 'id') === 'string' && typeof Reflect.get(value, 'text') === 'string'
}

// asCloseRecovery validates a server recovery before it controls any buttons.
export function asCloseRecovery(value: unknown): CloseRecovery | null {
  if (typeof value !== 'object' || value === null) return null
  const reason: unknown = Reflect.get(value, 'reason')
  const actions: unknown = Reflect.get(value, 'actions')
  const items: unknown = Reflect.get(value, 'items')
  if (!isCloseReason(reason) || !Array.isArray(actions) || !actions.every(isCloseAction) || !Array.isArray(items) || !items.every(isResponsibility)) return null
  return { reason, actions, items }
}

// closeRecoveryOf reads both a thrown API error and a bulk result row.
export function closeRecoveryOf(value: unknown): CloseRecovery | null {
  if (typeof value !== 'object' || value === null) return null
  return asCloseRecovery(Reflect.get(value, 'close'))
}

export const allowsForceClose = (value: unknown): boolean => closeRecoveryOf(value)?.actions.includes('force-close') ?? false

// BulkSessionItem is roomsvc.BulkSessionItem. transfer is set for a transfer.
export interface BulkSessionItem {
  session: string
  ok: boolean
  error?: string
  close?: CloseRecovery
  member?: Member
  transfer?: TransferState
}

// BulkSessionResult is roomsvc.BulkSessionResult.
export interface BulkSessionResult {
  action: string
  results: BulkSessionItem[] | null
  succeeded: number
  failed: number
}

// RoomSessionState is subagent.RoomSessionState: how a room's sessions run.
// pausedAt and resumedAt are the zero time "0001-01-01T00:00:00Z" when unset.
export interface RoomSessionState {
  roomId: string
  paused: boolean
  pausedAt: string
  resumedAt: string
  running: number
  held: number
  dormant: number
}

export interface RoomSessionResult {
  session: string
  ok: boolean
  status: string
  error?: string
}

// RoomSessionMutation is subagent.RoomSessionMutation, the answer of pause, resume and clear.
export interface RoomSessionMutation {
  state: RoomSessionState
  results: RoomSessionResult[] | null
  succeeded: number
  failed: number
}

// ConsolidationOptions is subagent.ConsolidationOptions, the request of ConsolidateSessions.
export interface ConsolidationOptions {
  sessions: string[]
  target: string
  profile?: string
  model?: string
}

// TransferOptions is subagent.TransferOptions as the web sends it.
// waitSeconds 0 falls back to the transcript at once; the most is 120.
export interface TransferOptions {
  profile: string
  waitSeconds: number
  model?: string
  retry?: boolean
}

// BulkSessionOptions is roomsvc.BulkSessionOptions.
export interface BulkSessionOptions {
  sessions: string[]
  action: string
  profile?: string
  model?: string
  force?: boolean
  parent?: string
}
