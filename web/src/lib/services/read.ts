// Readers for the engine results the Services page uses. Each takes unknown
// data and returns a typed value, or null where the call failed or the shape
// is wrong. Nothing here changes data.
import { arrayOf, isBoolean, isNumber, isRecord, isString } from '../api/guards.ts'
import type { HistoryRow, QueueRow, ReviewerInfo, ServiceRow, UsageRow } from './cards.ts'

const rows = (v: unknown): Record<string, unknown>[] => (Array.isArray(v) ? v.filter(isRecord) : [])
const str = (v: unknown): string => (isString(v) ? v : '')
const strings = (v: unknown): string[] => (arrayOf(isString)(v) ? v : [])

// ListServices: roomsvc.ServiceView.
export const readServices = (v: unknown): ServiceRow[] =>
  rows(v).map((r) => ({ role: str(r.role), profile: str(r.profile), pinned: str(r.pinned), reason: str(r.reason), profiles: strings(r.profiles) }))

// ReviewerStatus: watch.ReviewerStatus, with the first running review as current.
export function readReviewer(v: unknown): ReviewerInfo | null {
  if (!isRecord(v) || !isBoolean(v.enabled)) return null
  const current = rows(v.reviewers).map((r) => r.current).find(isRecord)
  return {
    enabled: v.enabled,
    profile: str(v.profile),
    lastError: str(v.lastError),
    current: current && isNumber(current.pr) ? { pr: current.pr, title: str(current.title) } : null,
    queued: Array.isArray(v.queue) ? v.queue.length : 0,
  }
}

// RoomMergeStatus: a list of watch.MergeQueueItem.
export const readQueue = (v: unknown): QueueRow[] | null => (Array.isArray(v) ? rows(v).map((r) => ({ state: str(r.state) })) : null)

// RoomMergeHistory: a list of watch.MergeHistoryEntry, newest first.
export const readHistory = (v: unknown): HistoryRow[] => rows(v).map((r) => ({ kind: str(r.kind), outcome: str(r.outcome), at: str(r.at), changes: Array.isArray(r.changes) ? r.changes.length : 0, detail: str(r.detail) }))

// Settings: configsvc.Item values, a list value shown as its JSON.
export function readSettings(v: unknown): Record<string, string> | null {
  if (!Array.isArray(v)) return null
  const out: Record<string, string> = {}
  for (const r of rows(v)) if (isString(r.key)) out[r.key] = isString(r.value) ? r.value : JSON.stringify(r.value ?? '')
  return out
}

// UsageRollup: subagent.UsageRollupSnapshot byRoleModel rows.
export function readUsage(v: unknown): UsageRow[] | null {
  if (!isRecord(v)) return null
  return rows(v.byRoleModel).map((r) => {
    const cost = isRecord(r.cost) ? r.cost : {}
    // The engine gives a non-null usd for a group with priced and unpriced turns; keep that it is partial.
    const incomplete = (isNumber(cost.partialTurns) && cost.partialTurns > 0) || (isNumber(cost.unknownTurns) && cost.unknownTurns > 0)
    return { role: str(r.role), usd: isNumber(cost.usd) ? cost.usd : null, incomplete }
  })
}

// VoiceStatus: voice.CallStatus.
export function readVoice(v: unknown): string | null {
  if (!isRecord(v)) return null
  if (v.available === false) return `unavailable: ${str(v.unavailable)}`
  return v.live === true ? 'live' : 'idle'
}
