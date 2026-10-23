// Callback shapes shared by the PR workspace.
import type { Row } from './prlist.ts'

export type ChangeAction = (change: Row) => void | Promise<void>
export type TrackAction = (change: Row, event: MouseEvent) => void | Promise<void>

// One item of a list row's action menu (RowActions).
export interface RowAction {
  label: string
  run: () => void
  hint?: string
  danger?: boolean
  gap?: boolean
}
