// The terminal dock's view state. It is per device and per room, so two
// devices can show the same shells differently; the shells live on the host.
import { isRecord } from '../api/guards.ts'

// A shell popped out of the dock: a floating window over the main area. x and
// y are px from its top left corner; z orders the windows (the largest is on
// top); min hides it to its chip in the bar.
export interface Rect { x: number; y: number; w: number; h: number }
export interface Area { w: number; h: number }
export interface Win extends Rect { id: string; z: number; min: boolean }

export interface DockView {
  open: boolean
  height: number // px, before the clamp to the main area
  maxed: boolean
  active: Record<string, string> // room or session id -> the open shell
  wins: Win[]
}

export const MIN_HEIGHT = 120
export const DEFAULT_HEIGHT = 280
export const MAX_SHARE = 0.8 // of the main area

export const closedDock = (): DockView => ({ open: false, height: DEFAULT_HEIGHT, maxed: false, active: {}, wins: [] })

// clampHeight keeps the dock between 120 px and 80% of the main area. A main
// area too small for both limits gets the minimum.
export function clampHeight(height: number, avail: number): number {
  const max = Math.max(MIN_HEIGHT, Math.floor(avail * MAX_SHARE))
  const h = Number.isFinite(height) ? Math.round(height) : DEFAULT_HEIGHT
  return Math.min(max, Math.max(MIN_HEIGHT, h))
}

export const MIN_WIN_W = 280
export const MIN_WIN_H = 160

// clampRect keeps a window inside the main area, shrinking it first when the
// area is smaller than the window. An area too small for the minimum size
// still gets the minimum, pinned to the top left.
export function clampRect(r: Rect, area: Area): Rect {
  const num = (n: number, fallback: number) => (Number.isFinite(n) ? Math.round(n) : fallback)
  const w = Math.min(Math.max(MIN_WIN_W, num(r.w, 520)), Math.max(MIN_WIN_W, Math.floor(area.w)))
  const h = Math.min(Math.max(MIN_WIN_H, num(r.h, 320)), Math.max(MIN_WIN_H, Math.floor(area.h)))
  return {
    w,
    h,
    x: Math.min(Math.max(0, num(r.x, 0)), Math.max(0, Math.floor(area.w) - w)),
    y: Math.min(Math.max(0, num(r.y, 0)), Math.max(0, Math.floor(area.h) - h)),
  }
}

// placeNew is where the n-th window opens: at the upper right, below the
// header, so the dock and bar stay clear; each one steps down and to the left
// so the earlier ones stay visible.
export function placeNew(area: Area, n: number): Rect {
  const w = Math.min(640, Math.floor(area.w * 0.6))
  const h = Math.min(380, Math.floor(area.h * 0.6))
  const step = 28 * (n % 8)
  return clampRect({ w, h, x: area.w - w - 32 - step, y: 56 + step }, area)
}

export type DockAction =
  | { type: 'show' }
  | { type: 'hide' }
  | { type: 'maximize' }
  | { type: 'resize'; height: number; avail: number }
  | { type: 'tab'; ref: string; id: string }
  | { type: 'pop'; id: string; area: Area }
  | { type: 'dockIn'; id: string }
  | { type: 'move'; id: string; x: number; y: number; area: Area }
  | { type: 'size'; id: string; w: number; h: number; area: Area }
  | { type: 'minimize'; id: string }
  | { type: 'raise'; id: string } // brings a window to the front and restores it
  | { type: 'prune'; keep: string[] } // drops windows of shells that are gone

export function dockReduce(v: DockView, a: DockAction): DockView {
  switch (a.type) {
    case 'show': return { ...v, open: true }
    case 'hide': return { ...v, open: false, maxed: false }
    case 'maximize': return { ...v, open: true, maxed: !v.maxed }
    case 'resize': return { ...v, height: clampHeight(a.height, a.avail), maxed: false }
    case 'pop': {
      const old = v.wins.find((x) => x.id === a.id)
      if (old) return dockReduce(v, { type: 'raise', id: a.id })
      return { ...v, wins: [...v.wins, { id: a.id, ...placeNew(a.area, v.wins.length), z: topZ(v) + 1, min: false }] }
    }
    case 'dockIn': return v.wins.some((x) => x.id === a.id) ? { ...v, wins: v.wins.filter((x) => x.id !== a.id) } : v
    case 'move': return setWin(v, a.id, (w) => ({ ...w, ...clampRect({ ...w, x: a.x, y: a.y }, a.area) }))
    case 'size': return setWin(v, a.id, (w) => ({ ...w, ...clampRect({ ...w, w: a.w, h: a.h }, a.area) }))
    case 'minimize': return setWin(v, a.id, (w) => ({ ...w, min: !w.min }))
    case 'raise': return setWin(v, a.id, (w) => (w.min || w.z < topZ(v) ? { ...w, min: false, z: topZ(v) + 1 } : w))
    case 'prune': return v.wins.every((w) => a.keep.includes(w.id)) ? v : { ...v, wins: v.wins.filter((w) => a.keep.includes(w.id)) }
    case 'tab': return v.active[a.ref] === a.id ? v : { ...v, active: { ...v.active, [a.ref]: a.id } }
  }
}

const topZ = (v: DockView): number => v.wins.reduce((z, w) => Math.max(z, w.z), 0)

function setWin(v: DockView, id: string, change: (w: Win) => Win): DockView {
  const w = v.wins.find((x) => x.id === id)
  if (!w) return v
  const next = change(w)
  return next === w ? v : { ...v, wins: v.wins.map((x) => (x === w ? next : x)) }
}

function winOf(saved: unknown): Win[] {
  if (!Array.isArray(saved)) return []
  const out: Win[] = []
  for (const w of saved) {
    if (!isRecord(w) || typeof w.id !== 'string' || out.some((x) => x.id === w.id)) continue
    const n = (k: string, d: number) => (typeof w[k] === 'number' && Number.isFinite(w[k]) ? w[k] : d)
    out.push({ id: w.id, x: n('x', 0), y: n('y', 0), w: n('w', 520), h: n('h', 320), z: n('z', out.length + 1), min: w.min === true })
  }
  return out
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
    wins: winOf(saved.wins),
  }
}

// legacyView maps the retired `terminal` view: the room opens on its feed
// with the dock open.
export function legacyView(view: string, fallback: string): { view: string; dock: boolean } {
  return view === 'terminal' ? { view: fallback, dock: true } : { view, dock: false }
}
