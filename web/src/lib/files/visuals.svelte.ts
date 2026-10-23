import type { Visual } from '../api/room.ts'
import { byVisual } from './visuals.ts'

// The open room's visuals, set by App whenever it loads them. Cards read it
// to show the newest version of their visual.
export const visuals = $state<{ room: string; byId: Record<string, Visual> }>({ room: '', byId: {} })

export function setVisuals(roomId: string, list: Visual[] | null | undefined) {
  visuals.room = roomId
  visuals.byId = byVisual(list)
}
