// Terminals. Source: internal/terminalsvc/terminal.go.

// TermInfo is terminalsvc.Info.
export interface TermInfo {
  title: string
  ref: string
  alive: boolean
  id: string
  roomId: string
  cwd: string
  shell: string
  cols: number
  rows: number
  created: string
  lastActivity: string
  exited: boolean
  exitCode: number
  sessionId?: string
}

// TermEvent is terminalsvc.Event. data is base64 PTY bytes, null when empty;
// start and offset are byte offsets. reset means the requested offset fell
// outside the kept buffer.
export interface TermEvent {
  id: string
  data: string | null
  start: number
  offset: number
  exited: boolean
  exitCode: number
  reset?: boolean
}

// ShellJob is terminalsvc.ShellJob, a background `dyna shell` command.
export interface ShellJob {
  id: string
  owner?: string
  command: string[]
  name?: string
  cwd: string
  state: 'starting' | 'running' | 'exited' | 'lost'
  code: number
  started: string
  ended?: string
  signal?: string
  stopBy?: 'agent' | 'owner' | 'cleanup'
  backgroundAt?: string
  outputLines?: number
  outputBytes?: number
  tail?: string
  logPruned?: boolean
}

// ShellLog is terminalsvc.ShellLog.
export interface ShellLog {
  text: string
  truncated: boolean
}

// ShellLogChunk is terminalsvc.ShellLogChunk: one byte range of a job's log.
export interface ShellLogChunk {
  text: string
  next: number
  size: number
}
