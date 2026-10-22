import { byVisual } from './visuals.js'

// The open room's visuals, set by App whenever it loads them. Cards read it
// to show the newest version of their visual.
export const visuals = $state({ room: '', byId: {} })

export function setVisuals(roomId, list) {
  visuals.room = roomId
  visuals.byId = byVisual(list)
}
