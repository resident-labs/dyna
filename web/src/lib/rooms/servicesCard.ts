// What the Home Services card shows: one row per service and per machine hook,
// with its state and last result. Failing rows come first and the healthy rows
// fold into one line. The functions are pure; ServicesCard.svelte loads the
// data and draws the result.
import type { HookRunLog, HookSummary } from '../api/settings.ts'
import type { RegistryService } from '../api/service.ts'
import { factsOf, pillOf } from '../services/model.ts'
import { listedHooks, runTone } from '../settings/hooks.ts'

// Where a row's log opens: a hook run shows its tail in the card, and a
// service opens the Services pane in Settings.
export type LogTarget =
  | { kind: 'hook'; hook: string; runId: string }
  | { kind: 'settings' }

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

// Only machine-wide registry services belong on Home. Room services remain in their room.
export function serviceRows(services: readonly RegistryService[]): ServiceCardRow[] {
  return services.filter((s) => s.scope === 'global').map((s) => {
    const pill = pillOf(s)
    return {
      id: `service:${s.id}`, name: s.label,
      tone: pill ? 'bad' : s.state === 'on' || s.state === 'running' ? 'ok' : 'idle',
      state: pill?.label || factsOf(s, Date.now(), !!s.switch).join(' · ') || 'Idle',
      at: s.lastRun ?? '', log: { kind: 'settings' },
    }
  })
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
  return `${count} ${failing ? 'other ' : ''}${count === 1 ? 'service' : 'services'}`
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
