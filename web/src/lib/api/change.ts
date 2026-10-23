// Pull requests and local changes. Source: internal/pr/view/change.go.

// CheckSummary is state.CheckSummary.
export interface CheckSummary {
  name: string
  state: string
  status: string
  conclusion: string
  url: string
  id?: number
  durationMs?: number
  node?: string
  completed?: string
  failureExcerpt?: string
  failureExcerptChecked?: boolean
  commentUrl?: string
}

export interface BotReview {
  bot: string
  state: string
  findings: number
  url: string
}

export interface Overlap {
  changeId: string
  // title names a change that has no PR number, such as a local change.
  title?: string
  head: string
  otherHead: string
  number?: number
  files?: string[]
  conflicts?: string[]
  mainHead?: string
  error?: string
}

// Owner-only HeldPRComments response; never part of ChangeView.
export interface HeldComment {
  version: string
  key: string
  roomId: string
  number: number
  id: string
  commenter: string
  body: string
  url: string
}

// ChangeView is view.ChangeView, one row of the changes list.
export interface ChangeView {
  isDraft: boolean
  created: string // zero time "0001-01-01T00:00:00Z" when unset
  checked: string // zero time when unset
  detailVersion: string
  queuePosition: number | null
  botReviews: BotReview[] | null
  checks: CheckSummary[] | null
  hasPolicyOverride?: boolean
  changeId?: string
  goalId?: string // the goal this change belongs to; omitted when none
  kind?: string
  branch?: string
  baseRef?: string
  base?: string
  reviewState?: string
  reviewBlocked?: string
  requiredReviewers?: string[] // canonical session IDs the owner requires
  missingRequiredReviewers?: string[] // required IDs with no approval at the current head
  key?: string
  repo?: string
  number?: number
  url?: string
  title?: string
  roomId?: string
  authorSession?: string
  reviewerSession?: string
  reviewPublishing?: string
  reviewerMode?: string
  reviewerName?: string
  state?: string
  watching?: boolean
  status?: string
  ci?: string
  ciTestReportNote?: string
  ciReason?: string
  consecutiveFailures?: number
  ciPaused?: boolean
  pauseReason?: string
  error?: string
  reviewRound?: number
  reviewHead?: string
  verdict?: string
  reviewAt?: string
  findingCount?: number
  hasReviewSummary?: boolean
  reviewUrl?: string
  head?: string
  heldCommentCount?: number
  filteredComments?: number
  overlaps?: Overlap[]
}

// AuthorNotes is state.AuthorNotes.
export interface AuthorNotes {
  intent: string
  testing: string
  risks: string
  questions: string
}

// FindingReply is state.FindingReply. at is an ISO time.
export interface FindingReply {
  disposition: string
  message: string
  head: string
  author: string
  at: string
}

// Finding is state.Finding.
export interface Finding {
  id: string
  severity: string
  file: string
  line: number
  why: string
  fix: string
  patch?: string
  replies: FindingReply[] | null
}

// ReviewRound is state.ReviewRound.
export interface ReviewRound {
  reviewer?: string
  reviewId?: number
  round: number
  head: string
  verdict: string
  summary: string
  findings: Finding[] | null
  at: string
}

// ChangePolicy is changepolicy.Policy. A nil Go slice arrives as null.
export interface ChangePolicy {
  reviewers?: string[]
  ciTimeBudget?: string
  ciBudgetExempt?: string[]
  ciWarnAfter?: string
  ciFailAfter?: string
  requiredChecks: string[] | null
  review: string
  ciNoJUnitWarning?: boolean
  ciTestMax?: string
  ciTestAllowNotes?: Record<string, string>
  ciTestAllow?: string[]
}

// PolicyOverride is changepolicy.Override. A null requiredChecks inherits the
// room list; an empty one overrides it.
export interface PolicyOverride {
  requiredChecks: string[] | null
  review?: string
  reason: string
}

// ChangeDetail is view.ChangeDetail: a change row with its review record.
export interface ChangeDetail extends ChangeView {
  summary: string
  notes: AuthorNotes
  findings: Finding[] | null
  reviewRounds: ReviewRound[] | null
  policy: ChangePolicy
  policyOverride: PolicyOverride
  skipReasons: string[] | null
}

// ReviewerSession and ReviewerHelper are watch.ReviewerSession and watch.ReviewerHelper.
export interface ReviewerSession {
  id: string
  address: string
  status: string
}

export interface ReviewerHelper extends ReviewerSession {
  profile: string
}

export interface ReviewCurrent {
  changeId?: string
  kind?: string
  pr: number
  title: string
  head: string
  startedAt: string
}

export interface ReviewQueued {
  changeId?: string
  kind?: string
  pr: number
  title: string
  head: string
}

export interface ReviewerSlot {
  session: ReviewerSession | null
  profile: string
  current: ReviewCurrent | null
  status: string
  lastError: string
}

// ReviewerStatus is watch.ReviewerStatus.
export interface ReviewerStatus {
  reviewers: ReviewerSlot[] | null
  helpers: ReviewerHelper[] | null
  enabled: boolean
  profile: string
  profileSource: string
  profileReason: string
  queue: ReviewQueued[] | null
  lastError: string
  recent: { changeId?: string; pr: number; verdict: string; at: string }[] | null
}

// MainWaitKind is watch.MainWaitKind: why the queue waits on main's CI.
export type MainWaitKind = 'no-push-ci' | 'no-head' | 'passed' | 'failed' | 'pending' | 'paused'

// MainWait is watch.MainWait: why the queue waits, as facts. The UI words the
// kind and shortens the commit, so no service sentence reaches the screen.
export interface MainWait {
  kind: MainWaitKind
  base?: string
}

// MainCI is watch.MainCI: the state of main's CI between batches.
export interface MainCI {
  state: string
  head: string
  wait?: MainWait
}

// MergeTrialStack is state.MergeTrialStack.
export interface MergeTrialStack {
  branch: string
  head?: string
  count: number
  phase: string
  reason?: string
  logs?: string
}

// MergeTrialStatus is watch.MergeTrialStatus.
export interface MergeTrialStatus {
  branch: string
  head?: string
  phase: string
  reason?: string
  batch: string[] | null
  maxParallel?: number
  stacks?: MergeTrialStack[]
}

// MergeQueueItem is watch.MergeQueueItem, one row of the room's merge status.
export interface MergeQueueItem {
  goalId?: string
  expedited?: boolean
  missingRequiredReviewers?: string[]
  title?: string
  author?: string
  roomId: string
  changeId: string
  url: string
  head: string
  state: string
  reason?: string
  readyAt?: string
  main?: MainCI
  failedSuite?: string
  excerpt?: string
  trial?: MergeTrialStatus
  overlaps?: Overlap[]
}

// MergeControlStatus is watch.MergeControlStatus. Local checks are keyed by head SHA.
export interface MergeControlStatus {
  roomId: string
  paused: boolean
  reason?: string
  expeditedGoalId?: string
  queue: MergeQueueItem[] | null
  trials: MergeTrialStatus[]
  localCIChecks?: Record<string, CheckSummary[]>
}

// NoiseRule is state.NoiseRule, a learned bot comment template. created and
// lastMatched are ISO times; lastMatched is omitted until a match.
export interface NoiseRule {
  id: string
  host: string
  repo: string
  bot: string
  template: string
  reason: string
  sourceUrl: string
  sourceComment: string
  learnedBy?: string
  created: string
  hits: number
  lastMatched?: string
}

// PRNoiseList is view.PRNoiseList.
export interface PRNoiseList {
  repositories: string[]
  rules: NoiseRule[]
  warning?: string
}

// MergeHistoryEntry is watch.MergeHistoryEntry, one row of the room's merge
// history. kind is trial, publish or decision.
export interface MergeHistoryEntry {
  kind: string
  outcome: string
  repo: string
  branch: string
  head: string
  changes: string[] | null
  at: string
  detail?: string
}

// CIBudgetSettings is view.CIBudgetSettings.
export interface CIBudgetSettings {
  ciTimeBudget: string
  ciBudgetExempt: string[] | null
  ciWarnAfter: string
  ciFailAfter: string
  ciTestMax: string
  ciTestAllowNotes: Record<string, string> | null
  ciTestAllow: string[] | null
  ciNoJUnitWarning: boolean | null
}

// PRConfig is view.PRConfig.
export interface PRConfig {
  mergeMaxParallel: number
  githubAccess: string
  reviewPublishing: string
  reviewerProfile: string
  reviewerSession: string
}
