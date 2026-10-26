import { ago } from '../ui/format.ts'

// RunHeaderInput is what the run header's one line reads: the run's state and
// times, and its agent counts.
export interface RunHeaderInput {
  status: string
  startedAt: string
  endedAt?: string
  working: number
  failed: number
  agents: number
}

// spanText words a length of time at the size a glance needs: 45s, 48m, 2h 05m.
export function spanText(ms: number): string {
  const s = Math.max(0, Math.floor(ms / 1000))
  if (s < 60) return `${s}s`
  if (s < 3600) return `${Math.floor(s / 60)}m`
  return `${Math.floor(s / 3600)}h ${String(Math.floor((s % 3600) / 60)).padStart(2, '0')}m`
}

const plural = (n: number, noun: string): string => `${n} ${noun}${n === 1 ? '' : 's'}`

// What a run that ended is called in its header.
const ended: Record<string, string> = { ok: 'Finished', error: 'Failed', canceled: 'Canceled', interrupted: 'Interrupted' }

// runLine is the run header's one line. A running run says how long it has run
// and how many agents work; a run that ended says when and how long it took.
// Failed agents show only when there are some.
export function runLine(run: RunHeaderInput, now: number): string {
  const start = new Date(run.startedAt).getTime()
  const failed = run.failed > 0 ? [`${run.failed} failed`] : []
  if (run.status === 'running') {
    const counts = run.agents > 0 ? [`${run.working} working`, plural(run.agents, 'agent')] : []
    return [`Running for ${spanText(now - start)}`, ...counts, ...failed].join(' · ')
  }
  const end = run.endedAt ? new Date(run.endedAt).getTime() : 0
  if (!end || !start) return [`Started ${ago(run.startedAt)}`, ...failed].join(' · ')
  return [`${ended[run.status] ?? 'Ended'} ${ago(run.endedAt)}`, `took ${spanText(end - start)}`, ...failed].join(' · ')
}

// startedTitle is the exact start time, for the line's tooltip.
export function startedTitle(startedAt: string): string {
  const t = new Date(startedAt)
  return Number.isNaN(t.getTime()) ? '' : `Started ${t.toLocaleString([], { dateStyle: 'medium', timeStyle: 'medium' })}`
}
