// Pure rules for the floating computer preview and for when the Computer tab
// holds a stream. No DOM, so node tests can run them.
import type { ComputerInfo } from '../api/computer.ts'
import { isRecord } from '../api/guards.ts'

export const PHONE = 700 // px: below this width there is no floating preview

// ComputerState is the part of a computer that the preview rules read.
type ComputerState = Pick<ComputerInfo, 'state' | 'shotAt'>

// previewVisible says whether the floating preview shows. It shows in the
// Messages view of a session whose computer runs or starts, or is stopped
// with a last picture. The owner can hide it; it comes back when the
// computer changes state.
export function previewVisible({ c, tab, view, width, hidden }: {
  c?: ComputerState | null
  tab: string
  view: string
  width: number
  hidden?: Hidden | null
}): boolean {
  if (!c || c.state === 'none' || c.state === 'idle') return false
  if (c.state === 'stopped' && !c.shotAt) return false
  if (tab !== 'chat' || view === 'computers') return false
  if (width < PHONE) return false
  return hidden?.state !== c.state
}

// inUse: the session's computer runs or starts, so its header icon shows.
export const inUse = (c?: ComputerState | null): boolean => !!c && (c.state === 'running' || c.state === 'starting')

export type Corner = 'tl' | 'tr' | 'bl' | 'br'
export const CORNERS: readonly Corner[] = ['tl', 'tr', 'bl', 'br']

// Which corner of a box (w by h) a point is closest to.
export function snapCorner(x: number, y: number, w: number, h: number): Corner {
  const top = y < h / 2
  const left = x < w / 2
  if (top) return left ? 'tl' : 'tr'
  return left ? 'bl' : 'br'
}

type Hidden = { state: string }
interface Preview { corner: Corner, minimized: boolean, hidden: Hidden | null }
export const DEFAULT_PREVIEW: Preview = { corner: 'br', minimized: false, hidden: null }

// KeyValueStore is the part of localStorage that the saved choices use.
type KeyValueStore = Pick<Storage, 'getItem' | 'setItem'>

const key = (session: string) => `dyna.computer.preview.${session}`
const isCorner = (v: unknown): v is Corner => CORNERS.some((c) => c === v)

// loadPreview reads one session's saved choices. Bad data gives defaults.
export function loadPreview(session: string, store: KeyValueStore | null = globalThis.localStorage): Preview {
  try {
    const v: unknown = JSON.parse(store?.getItem(key(session)) ?? 'null')
    if (!isRecord(v)) return { ...DEFAULT_PREVIEW }
    const { corner, minimized, hidden } = v
    return {
      corner: isCorner(corner) ? corner : DEFAULT_PREVIEW.corner,
      minimized: minimized === true,
      hidden: isRecord(hidden) && typeof hidden.state === 'string' ? { state: hidden.state } : null,
    }
  } catch {
    return { ...DEFAULT_PREVIEW }
  }
}

export function savePreview(session: string, p: Preview, store: KeyValueStore | null = globalThis.localStorage): void {
  try {
    store?.setItem(key(session), JSON.stringify(p))
  } catch {}
}

// streamWanted says whether the Computer tab should hold a stream now. It
// never holds one for a computer with no window, or while the page is
// hidden, so leaving the tab or the window releases the viewer lease.
export const streamWanted = ({ tab, c, pageVisible }: { tab: string, c?: ComputerState | null, pageVisible?: boolean }): boolean => tab === 'computer' && pageVisible !== false && c?.state === 'running'

// Badge text over the picture while the stream is not live.
export function badgeText(phase: string, shown?: boolean): string {
  if (phase === 'connected') return shown ? 'Live' : ''
  if (phase === 'connecting') return 'Connecting…'
  if (phase === 'failed') return 'Could not connect'
  return 'Stream ended'
}

// freshDue says whether a visible picture should ask the daemon for a new
// screenshot now. It asks for a running computer, only while the page is
// visible, and at most once per `every` ms. The server limits real captures
// to one per 5 s per computer as well.
export const freshDue = ({ phase, visible, last, now, every }: { phase: string, visible: boolean, last: number, now: number, every: number }): boolean => phase === 'running' && visible && now - last >= every
