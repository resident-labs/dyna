// Workflow runs. Source: internal/roomsvc/runcontrol.go.
import type { Doing } from './room.ts'

// RunSummary is roomsvc.RunSummary. endedAt is omitted while a run goes.
export interface RunSummary {
  id: string
  name: string
  status: string // running|ok|error|canceled|interrupted
  startedAt: string
  endedAt?: string
  roomId: string
  parent?: string
  parentId?: string
  detached?: boolean
  paused?: boolean
  error?: string
  agents: number
  done: number
  failed: number
  working: number
}

// RunList is roomsvc.RunList.
export interface RunList {
  runs: RunSummary[]
  total: number
  running: number
}

// RunAgent is roomsvc.RunAgent. started is unix milliseconds.
export interface RunAgent {
  id: number
  label: string
  profile?: string
  phase?: string
  // lane and stage are what the script gave its phase; both are absent when it gave none.
  lane?: string
  stage?: string
  status: string
  started?: number
  durMs?: number
  preview?: string
  doing?: Doing
  error?: string
  cached?: boolean
  // session is the session that ran the agent; an agent of an older run has none.
  session?: string
}

// RunCommand is roomsvc.RunCommand: one run() command call. Its id counts
// commands, not agents, so an agent 1 and a command 1 can both exist.
export interface RunCommand {
  id: number
  label: string
  phase?: string
  // lane and stage are what the script gave its phase; both are absent when it gave none.
  lane?: string
  stage?: string
  status: string // running|ok|error|unknown
  started?: number
  // command is the command line the script asked run() to execute.
  command?: string
  // shell is the managed shell that ran it.
  shell?: string
  durMs?: number
  // exitCode is absent until the command ended.
  exitCode?: number
  cached?: boolean
  preview?: string
  // tail is the last lines of the command's output.
  tail?: string
  error?: string
}

// RunLog is roomsvc.RunLog.
export interface RunLog {
  ts: number
  msg: string
}

// RunDetail is roomsvc.RunDetail.
export interface RunDetail {
  run: RunSummary
  agents: RunAgent[]
  commands: RunCommand[]
  agentTotal: number
  agentRunning: number
  agentFailed: number
  commandTotal: number
  commandRunning: number
  commandFailed: number
  phases?: string[]
  logs: RunLog[]
  resultSummary?: string
  resultBytes?: number
}

// RunJournalEntry is roomsvc.RunJournalEntry.
export interface RunJournalEntry {
  ts: number
  kind: string
  message: string
  next?: string
}

// RunResultView is roomsvc.RunResultView.
export interface RunResultView {
  result: string
  bytes: number
  truncatedKB?: number
  journal?: RunJournalEntry[]
}

// WorkflowInfo is workflowcatalog.Info.
export interface WorkflowInfo {
  name: string
  scope: 'project' | 'personal' | 'bundled'
  description: string
}

// StartNamedRunRequest is roomsvc.StartNamedRunRequest.
export interface StartNamedRunRequest {
  room: string
  name: string
  input: unknown
  resultTo?: string
}

// StartedRun is roomsvc.StartedRun.
export interface StartedRun {
  id: string
}

// RunSource is roomsvc.RunSource.
export interface RunSource {
  script?: string
}
