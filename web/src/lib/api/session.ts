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
export interface Item {
  postId?: string
  delivered?: boolean
  kind: string
  ts: string
  text?: string
  from?: string
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
  n: number
  status: string
  started: string
  ended: string
  usage: Usage
  items: Item[] | null
  omitted?: boolean
}

// Session is roomsvc.Session: a member's drill-down.
export interface Session {
  cursor?: string
  earlier: boolean
  newer: boolean
  member: Member
  address: string
  posts: Post[] | null
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

// QueueOrigin is subagent.QueueOrigin.
export interface QueueOrigin {
  session: string
  address: string
  state: string
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
  state: string
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

// BulkSessionItem is roomsvc.BulkSessionItem. transfer is set for a transfer.
export interface BulkSessionItem {
  session: string
  ok: boolean
  error?: string
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
