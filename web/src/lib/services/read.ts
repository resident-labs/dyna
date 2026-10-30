// Readers for the engine results the Services page uses. Each takes unknown
// data and returns a typed value, or null where the call failed or the shape
// is wrong. Nothing here changes data.
import { arrayOf, isBoolean, isNumber, isRecord, isString } from '../api/guards.ts'
import { IDReviews, IDMergeQueue, IDLocalCI, IDPRWatcher, IDLaneSync, IDIssueWatch, IDLibrary, IDParentCheckIns, IDVoice, IDUpdates, IDLoadDoctor, IDMaintenance, IDAccountUsage, IDHarnessVersions, GroupPullRequests, GroupAgents, GroupThisComputer, type ID, type Group, StateOn, StateRunning, StateWaiting, StateIdle, StateOff, StateError, StateNotSetUp, DetailNone, DetailLastRunFailed, DetailNoRunYet, DetailProfileWaiting, DetailWorking, DetailQueued, DetailPaused, DetailInCall, DetailUnavailable, DetailUnreadable, DetailUpdateSourceInvalid, DetailMultipleUpdateHooks, type Detail, type State } from '../api/service.gen.ts'
import type { RegistryService } from '../api/service.ts'
import type { HookRunRow, MergeHistoryRow, RunReason } from './activity.ts'

const rows = (v: unknown): Record<string, unknown>[] => (Array.isArray(v) ? v.filter(isRecord) : [])
const num = (v: unknown): number => (isNumber(v) ? v : 0)
const str = (v: unknown): string => (isString(v) ? v : '')
const strings = (v: unknown): string[] => (arrayOf(isString)(v) ? v : [])

// RoomMergeHistory: a list of watch.MergeHistoryEntry, newest first.
export const readHistory = (v: unknown): MergeHistoryRow[] => rows(v).map((r) => ({ kind: str(r.kind), outcome: str(r.outcome), at: str(r.at), changes: Array.isArray(r.changes) ? r.changes.length : 0, detail: str(r.detail), branch: str(r.branch), head: str(r.head), labels: strings(r.changes) }))

// HookRuns: a list of settings.HookRun, returned newest first.
export const readHookRuns = (v: unknown): HookRunRow[] =>
  rows(v).map((r) => ({ id: str(r.id), result: str(r.result), startedAt: str(r.startedAt), finishedAt: str(r.finishedAt) }))
    .sort((a, b) => Date.parse(b.startedAt) - Date.parse(a.startedAt))

// HookRunLog: one run's exit code, or -1 when the log records none. A log that
// records no exit code still names the cause on its last line, so the page
// reads that line as the reason.
export function readHookLog(v: unknown): RunReason | null {
  if (!isRecord(v) || !isNumber(v.exitCode)) return null
  const lines = str(v.tail).split('\n').map((l) => l.trim()).filter(Boolean)
  return { exitCode: v.exitCode, detail: v.exitCode >= 0 ? '' : lines.at(-1) ?? '' }
}

const states: readonly State[] = [StateOn, StateRunning, StateWaiting, StateIdle, StateOff, StateError, StateNotSetUp]
const details: readonly Detail[] = [DetailNone, DetailLastRunFailed, DetailNoRunYet, DetailProfileWaiting, DetailWorking, DetailQueued, DetailPaused, DetailInCall, DetailUnavailable, DetailUnreadable, DetailUpdateSourceInvalid, DetailMultipleUpdateHooks]
const ids: readonly ID[] = [IDReviews, IDMergeQueue, IDLocalCI, IDPRWatcher, IDLaneSync, IDIssueWatch, IDLibrary, IDParentCheckIns, IDVoice, IDUpdates, IDLoadDoctor, IDMaintenance, IDAccountUsage, IDHarnessVersions]
const groups: readonly Group[] = [GroupPullRequests, GroupAgents, GroupThisComputer]

// ServiceRegistry: serviceregistry.Service. A row with an id, group, state or
// detail this page does not know is dropped, so its service shows as unreadable.
export const readRegistry = (v: unknown): RegistryService[] | null =>
  Array.isArray(v)
    ? v.filter(isRecord).flatMap((r) => {
      const id = ids.find((x) => x === r.id)
      const group = groups.find((x) => x === r.group)
      const state = states.find((x) => x === r.state)
      const detail = details.find((x) => x === r.detail)
      if (id === undefined || group === undefined || state === undefined || detail === undefined) return []
      const sw = isRecord(r.switch) && isBoolean(r.switch.on) ? { on: r.switch.on } : undefined
      return [{
        id, group, state, detail, label: str(r.label), description: str(r.description), count: num(r.count),
        schedule: isRecord(r.schedule) ? { everyMinutes: num(r.schedule.everyMinutes), timeout: str(r.schedule.timeout) } : undefined,
        lastRun: str(r.lastRun) || undefined, nextRun: str(r.nextRun) || undefined, switch: sw,
        profile: str(r.profile), session: str(r.session), settingsKeys: strings(r.settingsKeys),
      }]
    })
    : null
