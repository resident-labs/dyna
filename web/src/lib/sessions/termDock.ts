// The terminal dock's view state. It is per device and per room, so two
// devices can show the same shells differently; the shells live on the host.
import { isRecord } from '../api/guards.ts'

export interface DockView {
  open: boolean
  height: number // px, before the clamp to the main area
  maxed: boolean
  active: Record<string, string> // room or session id -> the open shell
}

export const MIN_HEIGHT = 120
export const DEFAULT_HEIGHT = 280
export const MAX_SHARE = 0.8 // of the main area

export const closedDock = (): DockView => ({ open: false, height: DEFAULT_HEIGHT, maxed: false, active: {} })

// clampHeight keeps the dock between 120 px and 80% of the main area. A main
// area too small for both limits gets the minimum.
export function clampHeight(height: number, avail: number): number {
  const max = Math.max(MIN_HEIGHT, Math.floor(avail * MAX_SHARE))
  const h = Number.isFinite(height) ? Math.round(height) : DEFAULT_HEIGHT
  return Math.min(max, Math.max(MIN_HEIGHT, h))
}

export type DockAction =
  | { type: 'show' }
  | { type: 'hide' }
  | { type: 'maximize' }
  | { type: 'resize'; height: number; avail: number }
  | { type: 'tab'; ref: string; id: string }

export function dockReduce(v: DockView, a: DockAction): DockView {
  switch (a.type) {
    case 'show': return { ...v, open: true }
    case 'hide': return { ...v, open: false, maxed: false }
    case 'maximize': return { ...v, open: true, maxed: !v.maxed }
    case 'resize': return { ...v, height: clampHeight(a.height, a.avail), maxed: false }
    case 'tab': return v.active[a.ref] === a.id ? v : { ...v, active: { ...v.active, [a.ref]: a.id } }
  }
}

// dockOf reads a saved value; anything broken falls back to the closed dock.
export function dockOf(saved: unknown): DockView {
  if (!isRecord(saved)) return closedDock()
  const active: Record<string, string> = {}
  if (isRecord(saved.active)) for (const [k, id] of Object.entries(saved.active)) if (typeof id === 'string') active[k] = id
  return {
    open: saved.open === true,
    height: typeof saved.height === 'number' ? clampHeight(saved.height, Infinity) : DEFAULT_HEIGHT,
    maxed: saved.open === true && saved.maxed === true,
    active,
  }
}

// legacyView maps the retired `terminal` view: the room opens on its feed
// with the dock open.
export function legacyView(view: string, fallback: string): { view: string; dock: boolean } {
  return view === 'terminal' ? { view: fallback, dock: true } : { view, dock: false }
}
