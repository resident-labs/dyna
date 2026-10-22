import { ago } from '../ui/format.js'

// Pure helpers for the sync badge. The saved status of one repo is {remotes:
// {name: {ahead, behind, checkedAt, lastError, rewrite}}, integration:
// {head, lastExport, commits}, operation?: {kind, state, startedAt, head,
// prURL, error}}. origin is canonical.

// One remote's saved counts and last fetch result.
export interface RemoteStatus {
  ahead?: number
  behind?: number
  checkedAt?: string
  lastError?: string
  rewrite?: string
}

export type RemoteState = 'error' | 'diverged' | 'behind' | 'ahead' | 'synced'

// The saved refresh or export that the popover reports.
export interface SyncOperation {
  kind?: string
  state?: string
  startedAt?: string
  prURL?: string
  error?: string
  attempts?: number
}

export interface SyncRepository {
  remotes?: Record<string, RemoteStatus>
  integration?: { commits?: unknown[] }
  operation?: SyncOperation
}

// The badge's copy of one remote: its saved fields plus what it displays.
export interface Remote extends RemoteStatus {
  name: string
  canonical: boolean
  state: RemoteState
  label: string
}

export function remoteState(r: RemoteStatus): RemoteState {
  const ahead = r.ahead ?? 0
  const behind = r.behind ?? 0
  if (r.lastError) return 'error'
  if (r.rewrite || (ahead > 0 && behind > 0)) return 'diverged'
  if (behind > 0) return 'behind'
  if (ahead > 0) return 'ahead'
  return 'synced'
}

export function remoteLabel(r: RemoteStatus): string {
  if (r.lastError) return 'fetch failed'
  if (r.rewrite) return 'history rewritten'
  const parts: string[] = []
  if ((r.behind ?? 0) > 0) parts.push(`${r.behind} behind`)
  if ((r.ahead ?? 0) > 0) parts.push(`${r.ahead} ahead`)
  return parts.join(' · ') || 'in sync'
}

// Remotes for the popover: origin first, then by name.
export function remoteList(remotes: Record<string, RemoteStatus> | null | undefined): Remote[] {
  return Object.entries(remotes ?? {})
    .map(([name, r]) => ({ ...r, name, canonical: name === 'origin', state: remoteState(r), label: remoteLabel(r) }))
    .sort((a, b) => Number(b.canonical) - Number(a.canonical) || a.name.localeCompare(b.name))
}

export const opRunning = (op: SyncOperation | null | undefined): boolean =>
  ['building', 'testing', 'publishing'].includes(op?.state ?? '')

const word = (kind: string | undefined): string => (kind === 'refresh' ? 'Refresh' : 'Export')

// One line for the popover: what the saved operation is doing or how it ended.
// attempts counts publication tries (at most 5, a minute apart).
export function opLine(op: SyncOperation | null | undefined): string {
  if (!op) return ''
  const w = word(op.kind)
  const attempts = op.attempts ?? 0
  if (opRunning(op)) {
    const retry = op.error ? ` · ${attempts > 1 ? `attempt ${attempts} of 5, ` : ''}last error: ${op.error}` : ''
    return `${w} is running, started ${ago(op.startedAt ?? '')}${retry}`
  }
  if (op.state === 'failed') return `${w} failed${attempts > 1 ? ` after ${attempts} attempts` : ''}: ${op.error}`
  if (op.state === 'conflict') return `${w} stopped on a conflict`
  return `${w} done`
}

// The wait before the next status read: fast while an operation runs.
export const syncDelay = (op: SyncOperation | null | undefined): number => (opRunning(op) ? 5000 : 60000)

export const canExport = (repo: SyncRepository | null | undefined): boolean =>
  (repo?.integration?.commits?.length ?? 0) > 0 && !opRunning(repo?.operation)

// Place the popover inside the viewport. The badge can sit near the left of a
// narrow header, and a panel aligned only to its right edge then clips past the
// screen. left is the offset from the badge's own box; width keeps a gutter on
// both sides.
export function popoverPlacement(badgeLeft: number, badgeRight: number, viewport: number, width = 300, gutter = 8): { left: number; width: number } {
  const w = Math.min(width, viewport - 2 * gutter)
  const right = Math.min(badgeRight, viewport - gutter)
  return { left: Math.max(gutter, right - w) - badgeLeft, width: w }
}
