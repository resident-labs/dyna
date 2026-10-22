// GitHub pull requests and issues. Source: internal/pr/view/github.go and issues.go.

export interface GitHubUser {
  login: string
  name?: string
  isBot?: boolean
}

export interface GitHubLabel {
  name: string
  color: string
}

// GitHubLinkedPR is view.GitHubLinkedPR.
export interface GitHubLinkedPR {
  number: number
  state: string
  isDraft: boolean
  url: string
}

// GitHubItem is view.GitHubItem, one pull request or issue row. Time fields
// are ISO strings; closedAt and mergedAt are omitted until set.
export interface GitHubItem {
  workingSession?: string
  workingSessionName?: string
  workingSessionLive: boolean
  linkedPRs?: GitHubLinkedPR[]
  group?: string
  number: number
  title: string
  state: string
  stateReason?: string
  isDraft?: boolean
  author: GitHubUser
  labels: GitHubLabel[] | null
  assignees?: GitHubUser[]
  commentCount?: number
  createdAt: string
  updatedAt: string
  closedAt?: string | null
  mergedAt?: string | null
  url: string
  headRefName?: string
  baseRefName?: string
  reviewDecision?: string
  additions?: number
  deletions?: number
}

// GitHubListResult is view.GitHubListResult.
export interface GitHubListResult {
  account?: string
  repo: string
  url: string
  kind: string
  state: string
  items: GitHubItem[] | null
  more: boolean
}

export interface GitHubComment {
  author: GitHubUser
  body: string
  createdAt: string
  url?: string
}

export interface GitHubReview {
  author: GitHubUser
  body: string
  state: string
  submittedAt: string
  url?: string
}

// GitHubCheck is view.GitHubCheck. result is pending, success, failure,
// skipped or neutral.
export interface GitHubCheck {
  name: string
  workflow?: string
  result: string
  url?: string
}

export interface GitHubFile {
  path: string
  additions: number
  deletions: number
  changeType: string
}

export interface GitHubCommit {
  sha: string
  headline: string
  author: GitHubUser
  date: string
}

// GitHubDetail is view.GitHubDetail.
export interface GitHubDetail extends GitHubItem {
  kind: string
  body: string
  comments: GitHubComment[] | null
  reviews: GitHubReview[] | null
  checks: GitHubCheck[] | null
  files: GitHubFile[] | null
  commits: GitHubCommit[] | null
  changedFiles?: number
  mergeable?: string
}
