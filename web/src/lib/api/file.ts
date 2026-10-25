// Workspace files and the folder picker. Sources: internal/filesvc/workspace.go
// and internal/filesvc/browse.go.

// DirectoryEntry is filesvc.DirectoryEntry.
export interface DirectoryEntry {
  name: string
  path: string
  kind: string
  size: number
  modTime: string
  isGit: boolean
}

// DirectoryEntries is filesvc.DirectoryEntries.
export interface DirectoryEntries {
  root: string
  path: string
  entries: DirectoryEntry[] | null
}

// WorkspaceFile is filesvc.WorkspaceFile.
export interface WorkspaceFile {
  path: string
  name: string
  size: number
  language: string
  binary: boolean
  text: string
  url: string
  truncated: boolean
}

// BrowseInfo is filesvc.BrowseInfo.
export interface BrowseInfo {
  home: string
}

export interface BrowseEntry {
  name: string
  isGit: boolean
}

// Browse is filesvc.Browse.
export interface Browse {
  parent: string
  entries: BrowseEntry[] | null
  truncated?: boolean
}

// GitDiffResult is gitsvc.GitDiffResult, the diff text and whether it was cut.
export interface GitDiffResult {
  text: string
  truncated: boolean
}

// GitCommit is gitsvc.GitCommit. time is an ISO string.
export interface GitCommit {
  hash: string
  shortHash: string
  subject: string
  author: string
  time: string
  prNumber?: number
}

// GitFileStatus is gitsvc.GitFileStatus, one changed path.
export interface GitFileStatus {
  path: string
  originalPath: string
  status: string
  indexStatus: string
  worktreeStatus: string
  staged: boolean
  unstaged: boolean
  untracked: boolean
  conflicted: boolean
}

// GitStatusResult is gitsvc.GitStatusResult. worktree is true for a linked
// git worktree.
export interface GitStatusResult {
  commitCount: number
  changedFiles: number
  root: string
  branch: string
  oid: string
  upstream: string
  ahead: number
  behind: number
  files: GitFileStatus[] | null
  commits: GitCommit[] | null
  truncated: boolean
  worktree: boolean
}

// GitLastCommitsResult is gitsvc.GitLastCommitsResult: the last commit of each path asked.
export interface GitLastCommitsResult {
  commits: Record<string, GitCommit> | null
  truncated: boolean
}

// GitHistoryCommit is gitsvc.GitHistoryCommit. Go embeds GitCommit, so its
// fields sit at the top level.
export interface GitHistoryCommit extends GitCommit {
  added: number
  deleted: number
  moved: boolean
  path?: string
}

// GitHistoryResult is gitsvc.GitHistoryResult.
export interface GitHistoryResult {
  commits: GitHistoryCommit[] | null
  earlier: boolean
  cursor: string
  truncated: boolean
}

export interface GitBlameRange {
  startLine: number
  endLine: number
  commit: GitCommit
}

// GitBlameResult is gitsvc.GitBlameResult.
export interface GitBlameResult {
  ranges: GitBlameRange[] | null
  truncated: boolean
}

// PathCheck is roomsvc.PathCheck: what a typed path is for a new room. repo,
// name, roomId and roomName are set for some states.
export interface PathCheck {
  state: string
  repo?: string
  name?: string
  roomId?: string
  roomName?: string
}

// CreateRoomInput is roomsvc.CreateRoomInput. An empty name uses the repo name.
export interface CreateRoomInput {
  githubAccess?: 'read-only' | 'read-write'
  path: string
  name: string
  gitInit: boolean
}

// CreatedRoom is roomsvc.CreatedRoom. existed is true when the repo already had a room.
export interface CreatedRoom {
  id: string
  name: string
  repo: string
  existed: boolean
}
