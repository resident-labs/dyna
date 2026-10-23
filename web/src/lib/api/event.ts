// The live event stream at /api/events. Sources: roomsvc.Event
// (internal/roomsvc/session.go), listchange.Change
// (internal/listchange/change.go) and computerweb.Event.
import type { ComputerEvent } from './computer.ts'
import type { Post } from './room.ts'

// ListChange is listchange.Change: one changed row of a list. item is the
// row's own JSON for the list the event names.
export interface ListChange {
  op: string // upsert or delete
  id: string
  item?: unknown
  inHead?: boolean
  controls?: boolean
  consolidations?: boolean
}

export interface PostsEvent {
  kind: 'posts'
  roomId?: string
  posts?: Post[]
  offset?: number
}

export interface RoomsEvent {
  kind: 'rooms'
}

export interface NoticesEvent {
  kind: 'notices'
}

// ListEvent carries a room's list change, or only the room when the whole
// list changed.
export interface ListEvent {
  kind: 'members' | 'runs' | 'pins' | 'goals' | 'questions' | 'call-requests' | 'jobs' | 'changes' | 'prs' | 'issues' | 'session'
  roomId?: string
  change?: ListChange
}

// MachinesEvent says the machines registry changed: a row's settings or the
// live figures it reports.
export interface MachinesEvent {
  kind: 'machines'
}

// ResyncEvent is made by the client, not the engine, after a lost stream.
export interface ResyncEvent {
  kind: 'resync'
}

// BuildEvent is made by the client when a stream handshake reports a new
// engine build. The page can then load the bundle embedded by that engine.
export interface BuildEvent {
  kind: 'build'
  version: string
}

// The stream also carries the two room pause events.
export type StreamEvent = AppEvent | BuildEvent | { kind: 'room.paused' | 'room.resumed'; roomId?: string }

export type AppEvent = PostsEvent | RoomsEvent | NoticesEvent | ListEvent | ComputerEvent | MachinesEvent | ResyncEvent
