// Callback shapes shared by the PR workspace.
import type { Row } from './prlist.ts'

export type ChangeAction = (change: Row) => void | Promise<void>
export type TrackAction = (change: Row, event: MouseEvent) => void | Promise<void>
