import { prTracks } from './pr.ts'
import { asksChanges, isReady, isUntracked, type MergeItem, type Row } from './prlist.ts'
import type { Tone } from '../ui/tone.ts'

export interface NextAction {
  label: string
  tone?: Tone
  others: string[]
}

// Blockers use a fixed order; the remaining blockers stay available in the tooltip.
export function nextAction(row: Row, merge: MergeItem | null = null): NextAction {
  if (isUntracked(row)) return { label: 'Not tracked', others: [] }
  const blockers: { label: string; tone: Tone }[] = []
  if (row.ci === 'failed' || row.status === 'ci-failed' || merge?.state === 'ci-failed') blockers.push({ label: 'Blocked: CI failed', tone: 'danger' })
  if (merge?.state === 'conflict') blockers.push({ label: 'Blocked: conflict', tone: 'danger' })
  if (asksChanges(row) || merge?.state === 'changes') blockers.push({ label: 'Changes requested', tone: 'warning' })
  if (!row.isDraft && merge?.state === 'held') blockers.push({ label: 'Held', tone: 'warning' })
  if (row.ciPaused || row.status === 'paused' || row.pauseReason || merge?.state === 'paused') blockers.push({ label: 'Automation paused', tone: 'warning' })
  if (merge?.state === 'trial-failed' || merge?.state === 'trial-rejected') blockers.push({ label: 'Blocked: trial failed', tone: 'danger' })
  if (merge?.state === 'trial-unavailable') blockers.push({ label: 'Trial CI needed', tone: 'warning' })
  if (merge?.state === 'returned') blockers.push({ label: 'Returned to author', tone: 'warning' })
  if (merge?.state === 'stopped' || row.reviewState === 'blocked') blockers.push({ label: 'Review blocked', tone: 'warning' })
  if (merge?.state === 'read-only') blockers.push({ label: 'Read-only room', tone: 'warning' })
  const first = blockers[0]
  if (first) return { ...first, others: blockers.slice(1).map((b) => b.label) }
  if (row.isDraft) return { label: 'Draft', others: [] }
  if (merge?.state === 'waiting-dependency') return { label: 'Waiting for dependency', others: [] }
  if (merge?.state === 'waiting-base') return { label: 'Waiting for base PR', others: [] }
  if (merge?.state === 'waiting-repository') return { label: 'Waiting for repository', others: [] }
  if (merge?.trial || merge?.state.startsWith('trial-')) return { label: 'In merge trial', others: [] }
  if (merge?.state === 'ready' || (!merge && isReady(row))) return { label: 'Ready to merge', others: [] }
  if (prTracks(row).ci !== 'passed' && prTracks(row).ci !== 'absent') return { label: 'Waiting for CI', others: [] }
  return { label: 'Needs review', others: [] }
}
