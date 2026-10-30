import { prTracks } from './pr.ts'
import { asksChanges, isReady, isUntracked, type MergeItem, type Row } from './prlist.ts'
import type { Tone } from '../ui/tone.ts'

export interface NextAction {
  label: string
  kind: 'ci' | 'other'
  tone?: Tone
  others: string[]
}

// Blockers use a fixed order; the remaining blockers stay available in the tooltip.
export function nextAction(row: Row, merge: MergeItem | null = null): NextAction {
  if (isUntracked(row)) return { kind: 'other', label: 'Not tracked', others: [] }
  const blockers: { label: string; tone: Tone; kind?: 'ci' }[] = []
  if (row.ci === 'failed' || row.status === 'ci-failed' || merge?.state === 'ci-failed') blockers.push({ label: 'Blocked: CI failed', tone: 'danger', kind: 'ci' })
  if (merge?.state === 'conflict') blockers.push({ label: 'Blocked: conflict', tone: 'danger' })
  if (row.verdict === 'question') blockers.push({ label: 'Review question', tone: 'warning' })
  else if (asksChanges(row) || merge?.state === 'changes') blockers.push({ label: 'Changes requested', tone: 'warning' })
  if (!row.isDraft && merge?.state === 'held') blockers.push({ label: 'Held', tone: 'warning' })
  if (row.ciPaused || row.status === 'paused' || row.pauseReason || merge?.state === 'paused') blockers.push({ label: 'Automation paused', tone: 'warning' })
  if (merge?.state === 'trial-failed' || merge?.state === 'trial-rejected') blockers.push({ label: 'Blocked: trial failed', tone: 'danger' })
  if (merge?.state === 'trial-unavailable') blockers.push({ label: 'Trial CI needed', tone: 'warning' })
  if (merge?.state === 'returned') blockers.push({ label: 'Returned to author', tone: 'warning' })
  if (merge?.state === 'stopped' || row.reviewState === 'blocked') blockers.push({ label: 'Review blocked', tone: 'warning' })
  if (merge?.state === 'read-only') blockers.push({ label: 'Read-only room', tone: 'warning' })
  const first = blockers[0]
  if (first) return { ...first, kind: first.kind ?? 'other', others: blockers.slice(1).map((b) => b.label) }
  if (row.isDraft) return { kind: 'other', label: 'Draft', others: [] }
  if (merge?.state === 'waiting-dependency') return { kind: 'other', label: 'Waiting for dependency', others: [] }
  if (merge?.state === 'waiting-base') return { kind: 'other', label: 'Waiting for base PR', others: [] }
  if (merge?.state === 'waiting-repository') return { kind: 'other', label: 'Waiting for repository', others: [] }
  if (merge?.trial || merge?.state.startsWith('trial-')) return { kind: 'other', label: 'In merge trial', others: [] }
  if (merge?.state === 'ready' || (!merge && isReady(row))) return { kind: 'other', label: 'Ready to merge', others: [] }
  if (prTracks(row).ci === 'absent') return { kind: 'ci', label: 'CI missing', others: [] }
  if (merge?.state === 'ci' || prTracks(row).ci !== 'passed') return { kind: 'ci', label: 'Waiting for CI', others: [] }
  return { kind: 'other', label: 'Needs review', others: [] }
}
