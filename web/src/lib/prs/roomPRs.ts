// The room's open PR list and saved merge progress. Queue order comes from
// the merge service, never from inferred readiness in the browser.
import type { MergeControlStatus, MergeQueueItem, MergeTrialStatus } from '../api/change.ts'
import { checkSummary, isLocal, prTracks, type Meta } from './pr.ts'
import { isDone, type Row } from './prlist.ts'

export function openPRs(rows: Row[], queue: MergeQueueItem[] = []): Row[] {
  const order = new Map(queue.filter((q) => q.trial || q.state === 'ready').map((q, i) => [q.changeId, i]))
  return rows.filter((r) => !isLocal(r) && !isDone(r)).sort((a, b) => {
    const ai = order.get(a.changeId ?? '') ?? Infinity
    const bi = order.get(b.changeId ?? '') ?? Infinity
    return ai - bi || (b.number ?? 0) - (a.number ?? 0)
  })
}

export function mergePosition(id: string, queue: MergeQueueItem[]): string {
  const item = queue.find((q) => q.changeId === id)
  const batch = item?.trial?.batch ?? []
  const at = batch.indexOf(id)
  if (at >= 0) return `Trial · ${at + 1}/${batch.length}`
  const ready = queue.filter((q) => q.state === 'ready' && !q.trial)
  const next = ready.findIndex((q) => q.changeId === id)
  return next >= 0 ? `Next · ${next + 1}` : ''
}

export function reviewLabel(row: Row): Meta {
  if (row.reviewState === 'approved') {
    if (!row.head || !row.reviewHead) return { label: 'Approval unverified', tone: 'gold' }
    if (row.head !== row.reviewHead) return { label: 'Review outdated', tone: 'gold' }
    if (prTracks(row).missing.length) return { label: 'Needs required reviewers', tone: 'gold' }
    return { label: row.verdict === 'approve-with-nits' ? 'Approved with nits' : 'Approved', tone: 'sage' }
  }
  if (row.reviewState === 'changes') return { label: row.verdict === 'question' ? 'Review question' : 'Changes requested', tone: 'coral' }
  if (row.reviewState === 'reviewing') return { label: 'Reviewing', tone: 'iris' }
  if (row.reviewState === 'blocked') return { label: 'Review blocked', tone: 'gold' }
  if (row.reviewState === 'off') return { label: 'Review off', tone: '' }
  return { label: row.status === 'untracked' ? 'Not tracked' : 'Needs review', tone: '' }
}

export function ciLabel(row: Row): Meta {
  const nodes = [...new Set((row.checks ?? []).flatMap((c) => c.node ? [c.node] : []))]
  const source = nodes.length ? nodes.join(', ') : row.checks?.length ? 'GitHub CI' : 'CI'
  const ci = prTracks(row).ci
  return { label: `${source} · ${ci === 'running' ? 'running' : ci === 'absent' ? 'not required' : ci}`, tone: ci === 'passed' ? 'sage' : ci === 'failed' ? 'coral' : ci === 'running' ? 'gold' : '' }
}

export function trialProgress(trial: MergeTrialStatus, status: MergeControlStatus) {
  const checks = trial.head ? status.localCIChecks?.[trial.head] ?? [] : []
  const summary = checkSummary(checks)
  const nodes = [...new Set(checks.flatMap((c) => c.node ? [c.node] : []))]
  const completed = checks.filter((c) => c.status === 'completed' || ['success', 'failure', 'skipped', 'cancelled'].includes(c.state)).length
  const labels: Record<string, string> = { building: 'Building trial', reviewing: 'Reviewing trial', running: 'Testing trial', stacking: 'Testing prefixes', publishing: 'Publishing tested batch', published: 'Published · confirming main CI', failed: 'Trial failed', 'awaiting-root': 'Needs merge steward' }
  return { label: labels[trial.phase] ?? trial.phase, tone: ['failed', 'awaiting-root'].includes(trial.phase) ? 'coral' : trial.phase === 'published' ? 'sage' : 'gold', nodes: nodes.join(', '), total: summary.total, completed, passed: summary.passed.length, failed: summary.failed.length }
}

export function waitingReason(status: MergeControlStatus): string {
  if (status.paused) return status.reason || 'Merging is paused.'
  const main = status.queue?.find((q) => q.main && q.main.state !== 'passed')?.main
  return main?.reason || ''
}
