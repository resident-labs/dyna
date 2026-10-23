// Rooms, posts and sessions. Sources: internal/roomsvc/roomsvc.go,
// internal/roomsvc/spawn.go, internal/room/room.go, internal/room/attach.go.

// Room is roomsvc.Room.
export interface Room {
  id: string
  name: string
  displayName?: string
  repo?: string
  lastActivity: string
  unread: number
  running: number
  members: number
  pinned: boolean
  pinOrder: number
  archived: boolean
  paused?: boolean
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
  sha256: string
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

export interface PRNotice {
  commentId?: string
  kind: string
  number: number
  head: string
  changeId: string
}

// Forward is room.Forward, the snapshot kept in a forwarding post.
export interface Forward {
  id: string
  roomId: string
  ts: number
  from: string
  text: string
  kind?: string
  notice?: PRNotice
  human?: boolean
  attachments?: Attachment[]
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
  notice?: PRNotice
  human?: boolean
  source?: string
  spoken?: boolean
  relayOf?: string
  via?: string[]
  replyTo?: string
  forward?: Forward
  turn?: number
  ownerReport?: boolean
  attachments?: Attachment[]
}

// PostDetail is roomsvc.PostDetail.
export interface PostDetail {
  kind: string // shell, run, or session
  id: string
  turn?: number
}

// Post is roomsvc.Post. Go embeds room.Message, so its fields sit at the top
// level of the JSON.
export interface Post extends Message {
  owner: boolean
  completion: boolean
  fromHue: number
  speaker: string
  detail?: PostDetail
  preview?: string
  hidden?: number
  unread?: boolean
  fromEmoji?: string
  fullResult?: boolean
  resultCut?: boolean
  channels?: string[]
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
  channelUnread: Record<string, number> | null
}

// FeedSearch is roomsvc.FeedSearch: matching posts, newest first.
export interface FeedSearch {
  posts: Post[]
  more: boolean
}

// Doing is workeroutput.Activity: what a live session does now.
export interface Doing {
  text?: string
  since?: number // unix milliseconds
}

export interface Seat {
  external?: boolean
  harness?: string
  since?: string
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
  closeSummary?: string[]
  emoji?: string
  seat?: Seat
}

// Delivery is notify.Delivery.
export interface Delivery {
  to: string
  delivery: string
  turn?: number
  pointer?: boolean
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
export interface Question {
  id: string
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
