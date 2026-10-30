// Shell state and log helpers shared by timeline steps and the Shell tab.
import { elapsed } from '../ui/format.ts'
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

// commandOf is the bash -c script; a wrapper such as env puts it last.
export const commandOf = (job: ShellJob): string => job.command?.at(-1) ?? ''

export const titleOf = (job: ShellJob): string => job.name || commandOf(job)

// metaOf is the quiet text after the title: outcome, duration, who stopped it.
// A successful command names what happened; "exit 0" is a code a reader has to
// translate, so only a failure keeps the exit code.
export function metaOf(job: ShellJob, now: number): string {
  const state = blockState(job)
  const started = Date.parse(job.started)
  const span = job.ended ? elapsed(Date.parse(job.ended) - started) : ''
  const inBackground = job.backgroundAt ? 'succeeded in the background' : 'succeeded'
  switch (state) {
    case 'running':
      return elapsed(now - started)
    case 'background':
      return `in background · ${elapsed(now - started)}`
    case 'stopped':
      return ['stopped by ' + job.stopBy, job.signal, span].filter(Boolean).join(' · ')
    case 'failed':
      return job.state === 'lost' ? `lost · ${span}`.replace(/ · $/, '') : `failed with exit code ${job.code} · ${span}`
    default:
      return span ? (job.backgroundAt ? `${inBackground} · ${span}` : `succeeded in ${span}`) : inBackground
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
