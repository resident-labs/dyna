// What the Home Services card shows: one row per service and per machine hook,
// with its state and last result. Failing rows come first and the healthy rows
// fold into one line. The functions are pure; ServicesCard.svelte loads the
// data and draws the result.
import type { HookRunLog, HookSummary } from '../api/settings.ts'
import type { ServiceView } from '../api/service.ts'
import { listedHooks, runTone } from '../settings/hooks.ts'

// Where a row's log opens: a hook run shows its tail in the card, and a
// service opens the room's Services page.
export type LogTarget =
  | { kind: 'hook'; hook: string; runId: string }
  | { kind: 'room'; room: string }

export interface ServiceCardRow {
  id: string
  name: string
  tone: 'bad' | 'ok' | 'idle'
  // state is the plain words of the row: what the service is doing or how its last run ended.
  state: string
  // at is the time of the last result, or '' when there is none.
  at: string
  log: LogTarget | null
}

const resultWords: Record<string, string> = { pass: 'Passed', fail: 'Failed', error: 'Failed', pending: 'Running' }

// hookRows lists the machine's hooks that are not a service trigger. A hook
// that is off or has never run is quiet, not failing.
export function hookRows(summaries: readonly HookSummary[]): ServiceCardRow[] {
  const shown = new Set(listedHooks(summaries.map((s) => s.hook), 'global'))
  return summaries.filter((s) => shown.has(s.hook)).map((s): ServiceCardRow => {
    const run = s.lastRun
    const name = s.hook.label || s.hook.name
    const log: LogTarget | null = run ? { kind: 'hook', hook: s.hook.name, runId: run.id } : null
    if (s.hook.disabled) return { id: `hook:${s.hook.name}`, name, tone: 'idle', state: 'Off', at: run?.startedAt ?? '', log }
    if (!run) return { id: `hook:${s.hook.name}`, name, tone: 'idle', state: 'Never run', at: '', log }
    return { id: `hook:${s.hook.name}`, name, tone: runTone(run.result), state: resultWords[run.result] ?? run.result, at: run.startedAt, log }
  })
}

// RoomRead is one room's service list, or null when the read failed and no
// earlier read of that room exists. stale marks a list kept from an earlier
// read because the newest read failed.
export interface RoomRead { room: string; services: readonly ServiceView[] | null; stale?: boolean }

// settleReads keeps a room's last good list when a new read of it fails, so a
// failed read neither drops that room's failing service nor lets another room
// report it healthy. The kept list is marked stale, so the card still warns
// that it could not refresh. A room with no good read yet stays null.
export function settleReads(kept: Readonly<Record<string, readonly ServiceView[]>>, reads: readonly RoomRead[]): { byRoom: RoomRead[]; kept: Record<string, readonly ServiceView[]> } {
  const next: Record<string, readonly ServiceView[]> = {}
  const byRoom = reads.map(({ room, services }): RoomRead => {
    const settled = services ?? kept[room] ?? null
    if (settled) next[room] = settled
    return { room, services: settled, stale: services === null && settled !== null }
  })
  return { byRoom, kept: next }
}

// serviceRows turns each room's service list into one row per service. A
// service the engine cannot run (no usable profile) is failing in any room; the
// log opens the first room that has the problem, else the first room. A room
// that could not be read, or only shows its last read, is its own failing row.
export function serviceRows(byRoom: readonly RoomRead[]): ServiceCardRow[] {
  const rows = new Map<string, ServiceCardRow>()
  for (const { room, services, stale } of byRoom) {
    if (stale) rows.set(`stale:${room}`, { id: `service:stale:${room}`, name: 'Services', tone: 'bad', state: 'Could not refresh; showing the last read', at: '', log: { kind: 'room', room } })
    if (!services) {
      rows.set(`unreadable:${room}`, { id: `service:unreadable:${room}`, name: 'Services', tone: 'bad', state: 'Could not be read', at: '', log: { kind: 'room', room } })
      continue
    }
    for (const s of services) {
      const bad = !s.profile
      const old = rows.get(s.role)
      if (old && (old.tone === 'bad' || !bad)) continue
      const state = bad ? s.reason || 'No enabled profile' : [s.profile, s.sessions ? `${s.sessions} open` : ''].filter(Boolean).join(' · ')
      rows.set(s.role, { id: `service:${s.role}`, name: s.name, tone: bad ? 'bad' : 'ok', state, at: '', log: { kind: 'room', room } })
    }
  }
  return [...rows.values()]
}

// logKey names one run's log, so a newer run of the same hook never shows the
// text of an older one.
export const logKey = (target: Extract<LogTarget, { kind: 'hook' }>): string => `${target.hook}/${target.runId}`

// groupRows puts failing rows first, newest failure first, and keeps the rest
// together for the folded line.
export function groupRows(rows: readonly ServiceCardRow[]): { failing: ServiceCardRow[]; healthy: ServiceCardRow[] } {
  const failing = rows.filter((r) => r.tone === 'bad').sort((a, b) => Date.parse(b.at || '') - Date.parse(a.at || '') || 0)
  return { failing, healthy: rows.filter((r) => r.tone !== 'bad') }
}

// healthyLine is the folded line over the healthy rows.
export function healthyLine(count: number, failing: number): string {
  return `${count} ${failing ? 'others ' : ''}running normally`
}

// readLog is the text of a hook row's log: the tail of the run it names, or the
// reason the read failed, so a failed run always shows something.
export async function readLog(read: (hook: string, runId: string) => Promise<HookRunLog>, target: Extract<LogTarget, { kind: 'hook' }>): Promise<string> {
  try {
    return (await read(target.hook, target.runId)).tail || '(empty log)'
  } catch (e) {
    return e instanceof Error ? e.message : String(e)
  }
}
