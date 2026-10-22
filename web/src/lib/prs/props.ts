// Callback and prop shapes the PR components share.
import type { Snippet } from 'svelte'
import type { ChangePolicy, MergeQueueItem } from '../api/change.ts'
import type { Names, Row } from './prlist.ts'

export type ChangeAction = (change: Row) => void | Promise<void>
export type TrackAction = (change: Row, event: MouseEvent) => void | Promise<void>

// The status box and the action bar act on one change.
export interface StatusActions {
  onresume?: ChangeAction | null
  ontrack?: TrackAction | null
  onrules?: (() => void) | null
  onchange?: () => void | Promise<void>
}

// The props of PRStory; PRCard passes them on.
export interface StoryProps extends StatusActions {
  pr: Row
  names?: Names
  facts?: string | null
  merge?: MergeQueueItem | null
  review?: boolean
  room?: ChangePolicy | null
  roomId?: string
  onuntrack?: ChangeAction | null
  onreviewer?: (() => void) | null
  onread?: (() => void) | null
  onsession?: ChangeAction | null
  children?: Snippet | null
  actions?: Snippet | null
}
