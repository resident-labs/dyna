// Readers for the engine results the Services page uses. Each takes unknown
// data and returns a typed value, or null where the call failed or the shape
// is wrong. Nothing here changes data.
import { arrayOf, isBoolean, isNumber, isRecord, isString } from '../api/guards.ts'
import type { AccountsInfo, BudgetInfo, HookRow, MergeHistoryRow as HistoryRow, NoticeRow, QueueRow, ReviewerInfo, ServiceInfo as ServiceRow, TestHealth, UsageRow } from './model.ts'

const rows = (v: unknown): Record<string, unknown>[] => (Array.isArray(v) ? v.filter(isRecord) : [])
const num = (v: unknown): number => (isNumber(v) ? v : 0)
const str = (v: unknown): string => (isString(v) ? v : '')
const strings = (v: unknown): string[] => (arrayOf(isString)(v) ? v : [])

// ListServices: roomsvc.ServiceView.
export const readServices = (v: unknown): ServiceRow[] =>
  rows(v).map((r) => ({ role: str(r.role), name: str(r.name), profile: str(r.profile), pinned: str(r.pinned), reason: str(r.reason), profiles: strings(r.profiles) }))

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
    lastVerdictAt: rows(v.recent).map((r) => str(r.at)).sort().at(-1) ?? '',
  }
}

// RoomMergeStatus: a list of watch.MergeQueueItem.
export const readQueue = (v: unknown): QueueRow[] | null => (Array.isArray(v) ? rows(v).map((r) => ({ state: str(r.state), reason: str(r.reason), ...(isRecord(r.trial) ? { trial: { branch: str(r.trial.branch), batch: strings(r.trial.batch) } } : {}) })) : null)

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
    return { role: str(r.role), turns: num(r.turns), usd: isNumber(cost.usd) ? cost.usd : null, incomplete }
  })
}

// VoiceStatus: voice.CallStatus.
export function readVoice(v: unknown): string | null {
  if (!isRecord(v)) return null
  if (v.available === false) return `unavailable: ${str(v.unavailable)}`
  return v.live === true ? 'live' : 'idle'
}

// HookSummary: hooks.Summary rows, each with its newest run.
export function readHookSummary(v: unknown): HookRow[] | null {
  if (!Array.isArray(v)) return null
  return rows(v).map((r) => {
    const h = isRecord(r.hook) ? r.hook : {}
    const run = isRecord(r.lastRun) ? r.lastRun : null
    return {
      hook: { name: str(h.name), label: str(h.label), event: str(h.event), everyMinutes: num(h.everyMinutes), disabled: h.disabled === true },
      lastRun: run ? { result: str(run.result), startedAt: str(run.startedAt), finishedAt: str(run.finishedAt) } : null,
    }
  })
}

// Notices: roomsvc notices, of which the page reads provider limits.
export function readNotices(v: unknown): NoticeRow[] | null {
  if (!Array.isArray(v)) return null
  return rows(v).map((r) => ({ key: str(r.key), severity: str(r.severity), text: str(r.text) }))
}

// RoomCIBudget: the room's CI budget, as durations the page reads.
export function readBudget(v: unknown): BudgetInfo | null {
  if (!isRecord(v) || !isString(v.ciWarnAfter) || !isString(v.ciFailAfter) || !isString(v.ciTestMax)) return null
  return { warnAfter: str(v.ciWarnAfter), failAfter: str(v.ciFailAfter), testMax: str(v.ciTestMax) }
}

// AccountUsageCached: the stored account readings, of which the page shows the
// newest time and whether any reading is stale.
export function readAccounts(v: unknown): AccountsInfo | null {
  if (!isRecord(v) || !Array.isArray(v.accounts)) return null
  const accounts = rows(v.accounts)
  const times = accounts.map((a) => str(a.observedAt)).filter(Boolean).sort()
  return { count: accounts.length, at: times.at(-1) ?? '', stale: accounts.some((a) => a.stale === true) }
}

// CIHealth: the tests that got worse and are now over the limit, which is what
// the page's one line reports. A test counts when its latest stored run is over
// its limit and slower than the run before it; a test that rose but stays under
// the limit, one that improved, one with a single stored run and one the owner
// allowed are all left out, so the line is quiet unless something got worse.
export function readTestHealth(v: unknown): TestHealth | null {
  if (!isRecord(v) || !isNumber(v.limitMs)) return null
  const limit = v.limitMs
  let count = 0
  for (const t of rows(v.tests)) {
    if (str(t.ignored) !== '') continue
    const runs = rows(t.runs)
    const [last, before] = runs
    if (!last || !before) continue
    const nowMs = num(last.durationMs)
    const beforeMs = num(before.durationMs)
    if (nowMs > (num(t.limitMs) || limit) && nowMs > beforeMs) count++
  }
  return { count, limitMs: limit }
}

// MergeControlStatus retains the pause flag even when the queue is empty.
export function readMergeState(v: unknown): { queue: QueueRow[]; mergePaused: boolean } | null {
  if (!isRecord(v) || !isBoolean(v.paused)) return null
  const queue = readQueue(v.queue)
  return queue ? { queue, mergePaused: v.paused } : null
}
