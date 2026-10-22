// The terminal block of a Dyna shell command in a DM. Source of the job
// shape: terminalsvc.ShellJob (internal/terminalsvc/rooms.go).
import { duration, elapsed } from '../ui/format.js'
import type { ShellJob } from '../api/terminal.ts'

export type { ShellJob }

export type BlockState = 'running' | 'background' | 'done' | 'failed' | 'stopped'

// LogChunk is terminalsvc.ShellLogChunk.
export interface LogChunk {
  text: string
  next: number
  size: number
}

export function isLogChunk(value: unknown): value is LogChunk {
  return typeof value === 'object' && value !== null && 'text' in value && typeof value.text === 'string' &&
    'next' in value && typeof value.next === 'number' && 'size' in value && typeof value.size === 'number'
}

// mergeJob lays the live row over the snapshot a transcript item embeds. A
// job that ended long ago sends no tail, so the snapshot's stays.
export function mergeJob(snapshot: ShellJob, live: ShellJob | undefined): ShellJob {
  if (!live) return snapshot
  return { ...snapshot, ...live, tail: live.tail ?? snapshot.tail, outputBytes: live.outputBytes ?? snapshot.outputBytes }
}

export const isOpen = (job: ShellJob): boolean => job.state === 'running' || job.state === 'starting'

export function blockState(job: ShellJob): BlockState {
  if (isOpen(job)) return job.backgroundAt ? 'background' : 'running'
  if (job.state === 'lost') return 'failed'
  if (job.stopBy) return 'stopped'
  return job.code === 0 ? 'done' : 'failed'
}

// A quiet job succeeded and is over, so a run of them may fold into one line.
// A failed, stopped, running or background job never folds.
export const quiet = (job: ShellJob): boolean => blockState(job) === 'done' && !job.backgroundAt

// FOLD_AT is how many quiet commands in a row fold into one line.
export const FOLD_AT = 3

// groupMeta is the quiet text of a folded run: every exit is 0, and the total time.
export function groupMeta(jobs: ShellJob[]): string {
  const ms = jobs.reduce((sum, job) => sum + Math.max(0, Date.parse(job.ended ?? job.started) - Date.parse(job.started)), 0)
  return `all exit 0 · ${elapsed(ms)}`
}

// commandOf is the bash -c script; a wrapper such as env puts it last.
export const commandOf = (job: ShellJob): string => job.command.at(-1) ?? ''

export const titleOf = (job: ShellJob): string => job.name || commandOf(job)

export const tailLines = (job: ShellJob): string[] => (job.tail ? job.tail.split('\n') : [])

// Finished says how the end of a job reached the agent: a notice that opened
// turn N (woke), or one that arrived mid-turn.
export interface Finished {
  at: string | number
  turn?: number
  woke: boolean
}

// finishedText is the line a folded notice adds to its call's block.
export function finishedText(job: ShellJob, finished: Finished): string {
  const failed = blockState(job) === 'failed'
  const time = new Date(finished.at).toLocaleTimeString([], { hour: 'numeric', minute: '2-digit' })
  return [`${failed ? '✕ failed' : '✓ finished'} ${time}`, `exit ${job.code}`, job.ended ? duration(job.started, job.ended) : ''].filter(Boolean).join(' · ')
}

// jumpOf names the place the notice landed: the turn it opened, or the pointer.
export function jumpOf(job: ShellJob, finished: Finished): { text: string; target: string } {
  return finished.woke && finished.turn !== undefined
    ? { text: `woke turn ${finished.turn} ↓`, target: `turn-${finished.turn}` }
    : { text: 'told the agent mid-turn ↓', target: `note-${job.id}` }
}

// metaOf is the quiet text after the title: exit, duration, who stopped it.
// folded is set when a finished line says how the job ended.
export function metaOf(job: ShellJob, now: number, folded = false): string {
  const state = blockState(job)
  const started = Date.parse(job.started)
  const span = job.ended ? duration(job.started, job.ended) : ''
  if (folded && job.backgroundAt && !isOpen(job)) return `moved to background at ${elapsed(Date.parse(job.backgroundAt) - started)}`
  switch (state) {
    case 'running':
      return elapsed(now - started)
    case 'background':
      return `in background · ${elapsed(now - started)}`
    case 'stopped':
      return ['stopped by ' + job.stopBy, job.signal, span].filter(Boolean).join(' · ')
    case 'failed':
      return job.state === 'lost' ? `lost · ${span}`.replace(/ · $/, '') : `exit ${job.code} · ${span}`
    default:
      return `${job.backgroundAt ? 'finished in background · ' : ''}exit ${job.code} · ${span}`
  }
}

export const bytesOf = (n: number): string => (n < 1024 ? `${n} B` : `${Math.round(n / 1024)} KB`)

// sizeOf describes the output footer: lines once known, else bytes.
export function sizeOf(job: ShellJob): string {
  if (isOpen(job) || job.outputLines === undefined) return `${bytesOf(job.outputBytes ?? 0)} so far`
  return `${job.outputLines.toLocaleString('en-US')} line${job.outputLines === 1 ? '' : 's'}`
}

// ESCAPES matches the terminal control sequences of a log: CSI, OSC and string
// sequences (DCS, SOS, PM, APC) up to their terminator or the end of the text,
// and the short forms such as a character set choice (ESC ( B).
const ESCAPES = /\x1b\[[0-?]*[ -/]*[@-~]|\x1b[\]PX^_][^\x07\x1b]*(?:\x07|\x1b\\)?|\x1b[ -/]*[0-~]?/g

// plainLog shows joined log text as a terminal would: escape codes are gone,
// CRLF is a line end, and a line redrawn with carriage returns keeps what was
// drawn last.
export function plainLog(text: string): string {
  return text
    .replace(ESCAPES, '')
    .replaceAll('\r\n', '\n')
    .split('\n')
    .map((line) => line.slice(line.lastIndexOf('\r') + 1))
    .join('\n')
}
