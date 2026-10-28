import type { EventPayload, IssueState } from '../rooms/events.gen.ts'

import type { RunList } from './workflow.ts'
import type { Reference } from '../rooms/references.ts'
import type { Responsibilities } from '../rooms/responsibilities.ts'

// Rooms, posts and sessions. Sources: internal/roomsvc/roomsvc.go,
// internal/roomsvc/spawn.go, internal/room/room.go, internal/room/attach.go.

// Room is roomsvc.Room.
export interface Room {
  id: string
  name: string
  displayName?: string
  repo?: string
  rootProfile?: string
  // checkIn is the room's own parent check-in cadence, as typed: a duration
  // such as 30m, or off. Empty uses the hook's global interval.
  checkIn?: string
  // limits are the room's resource slice limits, as typed: a size such as
  // 2G for memory, a count such as 512 for tasks, or off. Empty uses the
  // machine default.
  limits?: RoomLimits
  lastActivity: string
  unread: number
  running: number
  members: number
  pinned: boolean
  pinOrder: number
  archived: boolean
  paused?: boolean
}

// RoomLimits is roomsvc.RoomLimits: one limit per field, as typed.
export interface RoomLimits {
  memoryHigh?: string
  memoryMax?: string
  tasksMax?: string
}

// RoomLimitField names one of a room's limits, as the engine call takes it.
export type RoomLimitField = keyof RoomLimits

// SetRoomLimitResult is roomsvc.SetRoomLimitResult: the saved value is the
// source of truth, and applyWarning names why the live slice did not pick it
// up, empty when it did.
export interface SetRoomLimitResult {
  applyWarning?: string
}

// Shot and Check are room.Shot and room.Check, the preview of an HTML card.
export interface Shot {
  width: number
  id: string
}

export interface Check {
  name: string
  status: string // pass, warn, fail or skip
  detail?: string
}

// Attachment is room.Attachment.
export interface Attachment {
  id: string
  name: string
  mime: string
  size: number
  // Prompt manifests omit checksums; stored API attachments include them.
  sha256?: string
  render?: string
  title?: string
  height?: number
  net?: boolean
  visual?: string
  version?: number
  banner?: string
  thumb?: string
  shots?: Shot[]
  checks?: Check[]
}

// EventNotice is room.EventNotice: the registered type of a system event
// post. The facts of the event are in its typed payload (see events.gen.ts).
export interface EventNotice {
  kind: string
  // producer names the engine service that observed the event (see
  // room.EventProducer). It is data, never a sender.
  producer?: string
}

// Forward is room.Forward, the snapshot kept in a forwarding post.
export interface Forward {
  id: string
  roomId: string
  ts: number
  from: string
  text: string
  kind?: string
  notice?: EventNotice
  human?: boolean
  references?: Reference[]
  attachments?: Attachment[]
}

// ScriptRef is room.ScriptRef: the workflow run behind a script's post.
export interface ScriptRef {
  runId: string
  workflow: string
}

// Message is room.Message. ts is unix milliseconds.
export interface Message {
  wake: boolean
  id: string
  ts: number
  roomId: string
  from: string
  to: string[] | null
  text: string
  sessionIds?: Record<string, string>
  kind?: string
  notice?: EventNotice
  // payload is the typed JSON of a sealed system event, generated from the Go
  // registry in events.gen.ts. Read it with an event helper, not by hand.
  payload?: EventPayload
  human?: boolean
  // script is the workflow run that wrote the post (room.ScriptRef).
  script?: ScriptRef
  // conversation is where the post was written: the room feed (or absent), one
  // session's DM (dm:<session id>), or the owner's voice call (voice).
  conversation?: string
  source?: string
  spoken?: boolean
  relayOf?: string
  via?: string[]
  replyTo?: string
  forward?: Forward
  turn?: number
  ownerReport?: boolean
  references?: Reference[]
  attachments?: Attachment[]
}

// PostDetail is roomsvc.PostDetail.
export interface PostDetail {
  kind: string // shell, run, or session
  id: string
  turn?: number
}

// ReplyTarget is roomsvc.ReplyTarget: who a reply answers. Address is the
// address the reply names; the room's own address is @root.
export interface ReplyTarget {
  kind: 'session' | 'room'
  id?: string
  // address is the address a reply names, resolved by the server: a session's
  // address today, or the room's. It is empty when the reply must name nobody.
  address?: string
}

// Post is roomsvc.Post. Go embeds room.Message, so its fields sit at the top
// level of the JSON.
export interface Post extends Message {
  owner: boolean
  completion: boolean
  fromHue: number
  speaker: string
  // fromRoom names the room the owner wrote this post from, when it is not this room.
  fromRoom?: string
  detail?: PostDetail
  // reply names who a reply to this post answers, read from the post's stored
  // sender identity. A post with no reply target keeps the draft's recipients.
  reply?: ReplyTarget
  preview?: string
  hidden?: number
  unread?: boolean
  fromEmoji?: string
  fullResult?: boolean
  resultCut?: boolean
  views?: string[]
  // forRoot marks a post addressed to the room's root session.
  forRoot?: boolean
  pinned?: boolean
  pinnedAt?: number // unix milliseconds
  pinnedBy?: string
}

// Feed is roomsvc.Feed.
export interface Feed {
  room: Room
  posts: Post[] | null
  offset: number
  earlier?: number
  newer?: number
  cursor?: string
  unread: number
  firstUnread?: string
  viewUnread: Record<string, number> | null
}

// FeedSearch is roomsvc.FeedSearch: matching posts, newest first.
export interface FeedSearch {
  posts: Post[]
  more: boolean
}

// DoingStep is workeroutput.Activity.Step: the kind of tool call a live
// session runs now, in the session view's own words. wait is Dyna's own
// shell_output wait, the one step a new message can end at once.
export type DoingStep = 'run' | 'wait' | 'read' | 'edit' | 'write' | 'search' | 'fetch' | 'tool'

// Doing is workeroutput.Activity: what a live session does now.
export interface Doing {
  text?: string
  since?: number // unix milliseconds
  step?: DoingStep
  // steerable says a new message for the session ends this step early
  // (Dyna's shell run or wait), so the session reads it at once.
  steerable?: boolean
}

export interface Seat {
  external?: boolean
  harness?: string
  since?: string
}

// CloseOutcomeKind is sessionclose.ResponsibilityKind: the kind of work one
// close result row is about. It is undefined when the failure concerns the
// close itself rather than one piece of work.
export type CloseOutcomeKind = 'pr' | 'goal' | 'item' | 'question' | 'child' | 'shell' | 'workflow'

// CloseOutcomeResult is sessionclose.OutcomeResult: what the close did with
// that work.
export type CloseOutcomeResult = 'moved' | 'closed' | 'canceled' | 'withdrawn' | 'returned' | 'stopped' | 'released' | 'deferred' | 'failed'

// CloseFailure is sessionclose.Failure: why one close step did not run.
export type CloseFailure = 'handoff' | 'goal-needs-agent' | 'child-heir' | 'child-handoff' | 'process-cleanup'

// CloseOutcome is sessionclose.Outcome: one typed fact of a session close. The
// app draws its label, link and choice from these fields, never from engine
// wording.
export interface CloseOutcome {
  kind?: CloseOutcomeKind
  result: CloseOutcomeResult
  /** The goal, item, run, question or shell ID, or a session ID for a child. */
  id?: string
  /** The PR number of PR work. */
  number?: number
  /** The display name of the work, such as a child session's name. */
  name?: string
  /** The address of the session that took the work; empty for top level. */
  to?: string
  failure?: CloseFailure
}

// Member is roomsvc.Member: the owner entry or a session in a room.
export interface Member {
  resumable: boolean
  name: string
  address: string
  depth: number
  children: number
  status: string
  state: string
  turns: number
  created: string
  lastActivity: string
  hue: number
  id?: string
  service?: boolean
  role?: string
  model?: string
  transfer?: string
  profile?: string
  parent?: string
  owner?: boolean
  ended?: string
  doing?: Doing
  closed?: boolean
  closeSummary?: CloseOutcome[]
  emoji?: string
  seat?: Seat
  responsibilities?: Responsibilities
  // ownerDisplay marks a session whose own shells use the owner's display.
  ownerDisplay?: boolean
}

// Delivery is notify.Delivery.
export interface Delivery {
  to: string
  delivery: string
  turn?: number
  pointer?: boolean
  voiceCall?: boolean
  error?: string
}

// PostResult is roomsvc.PostResult.
export interface PostResult {
  message: Message
  deliveries: Delivery[] | null
  failed?: Delivery[]
  sticky?: string
}

// SpawnResult is roomsvc.SpawnResult.
export interface SpawnResult {
  member: Member
  warning?: string
}

// SendResult is roomsvc.SendResult: the composer's one send call either
// posted or started a session.
export type SendResult =
  | { kind: 'post'; post: PostResult }
  | { kind: 'spawn'; spawn: SpawnResult }

// ProfileChoice is roomsvc.ProfileChoice, one profile a session can start with.
export interface ProfileChoice {
  name: string
  harness: string
  model?: string
  models?: string[]
  default?: boolean
}

// Skip is notify.Skip: a group member left out of a post.
export interface Skip {
  to: string
  reason: string
}

// SpawnPreview is roomsvc.SpawnPreview, what a "/<profile> <task>" draft would start.
export interface SpawnPreview {
  profile: string
  room: string
  harness?: string
  model?: string
  name?: string
  task?: string
  dir?: string
  error?: string
}

// Draft is roomsvc.Draft, a parsed composer draft.
export interface Draft {
  mentions: string[] | null
  text: string
  default: boolean
  wakes: string[] | null
  self: boolean
  skipped?: Skip[]
  spawn?: SpawnPreview
}

// QuestionAnswer and QuestionWithdrawal are questions.Answer and questions.Withdraw.
export interface QuestionAnswer {
  at: string
  by: string
  human?: boolean
  choice?: string
  text?: string
  // source is "card" when an HTML card wrote the answer and the owner confirmed it.
  source?: string
}

export interface QuestionWithdrawal {
  at: string
  by: string
  reason: string
  human?: boolean
}

// Question is roomsvc.Question. Go embeds questions.Question, so its fields
// sit at the top level of the JSON. attachments is null when the question's
// post has none.
// TerminalStage is questions.TerminalStage: where a terminal request stands.
export type TerminalStage = 'waiting-session' | 'waiting-owner' | 'denied' | 'done' | 'failed' | 'declined' | 'withdrawn'

// TerminalVerdict is questions.TerminalVerdict: how the owner ends a request.
export type TerminalVerdict = 'done' | 'failed' | 'declined'

// TerminalRequest is questions.TerminalRequest. sharedLines is set only when
// the owner chose to share the end of the screen.
export interface TerminalRequest {
  argv: string[] | null
  cwd: string
  reason: string
  stage: TerminalStage
  holder?: string
  termId?: string
  exitCode?: number
  sharedLines?: string
}

export interface Question {
  id: string
  // kind is "terminal" for a terminal request; a plain question has none.
  kind?: 'question' | 'terminal'
  terminal?: TerminalRequest
  room: string
  asker: string
  to: string
  text: string
  options: string[] | null
  meanwhile: string
  post: string
  state: string
  askedAt: string
  groupKey: string
  askerName: string
  askerHue: number
  askerEmoji: string
  withdrawnHue: number
  attachments: Attachment[] | null
  human?: boolean
  answer?: QuestionAnswer
  withdrawn?: QuestionWithdrawal
  askedTurn?: number
  answerDelivered?: boolean
  toName?: string
  withdrawnBy?: string
  answererName?: string
}

// VisualVersion and Visual are roomsvc.VisualVersion and roomsvc.Visual. The
// engine builds every visual with at least one version, so versions is never
// null.
export interface VisualVersion {
  post: Post
  attachment: Attachment
}

export interface Visual {
  id: string
  post: Post
  attachment: Attachment
  versions: VisualVersion[]
}

// Issue is room.Issue in internal/room/issues.go. filedAt and changedAt are
// unix milliseconds.
export interface Issue {
  id: string
  // number is the room's short issue number, shown as D-12.
  number: number
  title: string
  file: string
  line: number
  filer: string
  filedAt: number
  state: IssueState
  reason: string
  goalId: string
  githubUrl: string
  // fixPr is the open PR that closes this issue on merge; 0 or omitted when none.
  fixPr?: number
  changedBy: string
  changedAt: number
}

// SeatReturn is roomsvc.SeatReturn, the answer of ReturnSeat.
export interface SeatReturn {
  seat: Seat
  changed: boolean
  deliveries?: Delivery[]
}

// Opened is roomsvc.Opened: a room's first screen in one call.
export interface Opened {
  feed: Feed
  members: Member[] | null
  choices: string[] | null
  pins: Post[] | null
  runs: RunList
}
