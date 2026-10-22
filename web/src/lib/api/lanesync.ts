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
