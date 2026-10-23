// The integration branch of a repository. Source: internal/lanesync/operations.go.

// Commit is lanesync.Commit. at is an ISO time.
export interface IntegrationCommit {
  sha: string
  title: string
  author: string
  at: string
}

// Integration is lanesync.Integration: the commits integrated since the last export.
export interface Integration {
  head: string
  lastExport: string
  commits: IntegrationCommit[]
}

// Remote is lanesync.Remote, the saved state of one git remote. checkedAt is an ISO time.
export interface Remote {
  ahead: number
  behind: number
  remoteHead: string
  branch: string
  checkedAt: string
  lastError?: string
  rewrite?: string
}

// Progress is lanesync.Progress, a refresh or export in flight or done.
export interface Progress {
  kind: string
  state: string
  startedAt: string
  head: string
  prURL: string
  error: string
  attempts: number
}

// Repository is lanesync.Repository.
export interface Repository {
  remotes: Record<string, Remote> | null
  integration: Integration
  operation?: Progress
}

// SyncStatus is lanesync.Status: the repositories by path.
export type SyncStatus = Record<string, Repository>

// SyncStarted is watch.SyncStarted, the answer of a refresh or export request.
export interface SyncStarted {
  started: boolean
}
