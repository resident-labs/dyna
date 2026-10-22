// Package state owns the persisted PR schema and reads atomic snapshots.
package state

import (
	"dyna-agent/internal/changepolicy"
	"encoding/json"
	"time"
)

type ReviewResult struct {
	ChangeID string    `json:"changeId,omitempty"`
	PR       int       `json:"pr"`
	Verdict  string    `json:"verdict"`
	At       time.Time `json:"at"`
}

// Reviewer stores room settings and provider failure observations, never pool occupancy.
type Reviewer struct {
	Key              string               `json:"-"`
	Disabled         bool                 `json:"disabled,omitempty"`
	LastError        string               `json:"lastError,omitempty"`
	FailedProfiles   map[string]time.Time `json:"failedProfiles,omitempty"`
	ReviewPublishing string               `json:"reviewPublishing,omitempty"`
	Profile          string               `json:"profile,omitempty"`
}
type Notice struct {
	NeedsRoot bool `json:"needsRoot,omitempty"`
	System    bool `json:"system,omitempty"`
	// Kind is "ci" for CI results and budgets; empty for everything else.
	Kind         string `json:"kind,omitempty"`
	SupersededBy string `json:"supersededBy,omitempty"`
	ID           string `json:"id"`
	Target       string `json:"target,omitempty"`
	Text         string `json:"text"`
	// Other names the other change of a radar pair, so a later pass clears
	// that notice by its pair and not by its wording.
	Other  string `json:"other,omitempty"`
	PostID string `json:"postId,omitempty"`
	Done   bool   `json:"done"`
	Error  string `json:"error,omitempty"`
}
type AuthorNotes struct {
	Intent    string `json:"intent"`
	Testing   string `json:"testing"`
	Risks     string `json:"risks"`
	Questions string `json:"questions"`
}
type FindingReply struct {
	Disposition string    `json:"disposition"`
	Message     string    `json:"message"`
	Head        string    `json:"head"`
	Author      string    `json:"author"`
	At          time.Time `json:"at"`
}
type Finding struct {
	ID       string         `json:"id"`
	Severity string         `json:"severity"`
	File     string         `json:"file"`
	Line     int            `json:"line"`
	Why      string         `json:"why"`
	Fix      string         `json:"fix"`
	Patch    string         `json:"patch,omitempty"`
	Replies  []FindingReply `json:"replies"`
}
type ReviewRound struct {
	Reviewer string    `json:"reviewer,omitempty"`
	ReviewID int64     `json:"reviewId,omitempty"`
	Round    int       `json:"round"`
	Head     string    `json:"head"`
	Verdict  string    `json:"verdict"`
	Summary  string    `json:"summary"`
	Findings []Finding `json:"findings"`
	At       time.Time `json:"at"`
}

// TODO: legacy/shim remove later — reads saved landing history; remove after old records no longer need it.
type Landing struct {
	Checked     string              `json:"checked"`
	Actor       string              `json:"actor"`
	Policy      changepolicy.Policy `json:"policy"`
	SkipReasons []string            `json:"skipReasons"`
	State       string              `json:"state"`
	Base        string              `json:"base"`
	Head        string              `json:"head"`
	Merged      string              `json:"merged"`
	Conflicts   []string            `json:"conflicts"`
	Reason      string              `json:"reason"`
	At          time.Time           `json:"at"`
}

// PendingVerdict is one saved review in the PR's ordered pending list.
type PendingVerdict struct {
	Next     *PendingVerdict `json:"next,omitempty"`
	Actor    string          `json:"actor"`
	Head     string          `json:"head"`
	Verdict  string          `json:"verdict"`
	Summary  string          `json:"summary"`
	ReviewID int64           `json:"reviewId,omitempty"`
	Findings []Finding       `json:"findings"`
}

type Record struct {
	PendingVerdict *PendingVerdict `json:"pendingVerdict,omitempty"`
	// ReleaseReviewersAfterReplay holds closed reviewers until their saved verdicts replay.
	ReleaseReviewersAfterReplay []string           `json:"releaseReviewersAfterReplay,omitempty"`
	MergeTrialFailure           *MergeTrialFailure `json:"mergeTrialFailure,omitempty"`
	IsDraft                     bool               `json:"isDraft,omitempty"`
	Mergeable                   *bool              `json:"mergeable,omitempty"`
	DefaultBranch               string             `json:"defaultBranch,omitempty"`
	ChecksAt                    time.Time          `json:"checksAt,omitempty"`

	CIBudgetHead   string            `json:"ciBudgetHead,omitempty"`
	CIBudgetErrors map[string]string `json:"ciBudgetErrors,omitempty"`
	CIBudgetRuns   map[string]string `json:"ciBudgetRuns,omitempty"`
	CIBudgetNotes  map[string]string `json:"ciBudgetNotes,omitempty"`
	// CIBudgetInputs identifies completed measurements using the existing HTTP cache.
	CIBudgetInputs map[string]string `json:"ciBudgetInputs,omitempty"`
	// CIBudgetSteps marks verdicts measured from runner step execution.
	CIBudgetSteps map[string]bool `json:"ciBudgetSteps,omitempty"`
	// CIQueueWait is time jobs waited for a runner, per run. It never counts against the budget.
	CIQueueWait       map[string]string     `json:"ciQueueWait,omitempty"`
	PolicyOverride    changepolicy.Override `json:"policyOverride"`
	Landing           *Landing              `json:"landing,omitempty"`
	ChangeID          string                `json:"changeId,omitempty"`
	Kind              string                `json:"kind,omitempty"`
	RepoPath          string                `json:"repoPath,omitempty"`
	BaseRef           string                `json:"baseRef,omitempty"`
	BaseRetargetSeen  time.Time             `json:"baseRetargetSeen,omitempty"` // a base change that waits for the parent's merge
	Notes             AuthorNotes           `json:"notes"`
	Category          string                `json:"category,omitempty"`
	Dependencies      []string              `json:"dependencies,omitempty"`
	Findings          []Finding             `json:"findings"`
	RequiredReviewers []string              `json:"requiredReviewers,omitempty"`
	ReviewRounds      []ReviewRound         `json:"reviewRounds"`
	CITestPolicy      string                `json:"ciTestPolicy,omitempty"`
	CITestReportNote  string                `json:"ciTestReportNote,omitempty"`
	CINoJUnitWarned   bool                  `json:"ciNoJUnitWarned,omitempty"`
	CITestHead        string                `json:"ciTestHead,omitempty"`
	CITestErrors      map[string]string     `json:"ciTestErrors,omitempty"`
	CITestFailures    map[string]string     `json:"ciTestFailures,omitempty"`
	CITestRuns        map[string]string     `json:"ciTestRuns,omitempty"`
	CITestRetries     map[string]int        `json:"ciTestRetries,omitempty"`

	CIStaleCleanupHead string `json:"ciStaleCleanupHead,omitempty"`

	Polling          Polling                      `json:"polling"`
	RateLimit        RateLimit                    `json:"rateLimit"`
	QuotaAccount     string                       `json:"quotaAccount,omitempty"`
	Key              string                       `json:"key"`
	Host             string                       `json:"host"`
	Repo             string                       `json:"repo"`
	Number           int                          `json:"number"`
	URL              string                       `json:"url"`
	Title            string                       `json:"title"`
	RoomID           string                       `json:"roomId"`
	Author           string                       `json:"authorSession"`
	FormerAuthors    []string                     `json:"formerAuthors,omitempty"`
	ReviewHistory    []map[string]json.RawMessage `json:"reviewHistory,omitempty"`
	Parent           string                       `json:"parentSession,omitempty"`
	Dir              string                       `json:"dir"`
	Branch           string                       `json:"branch,omitempty"`
	Base             string                       `json:"base,omitempty"`
	LookupDir        string                       `json:"lookupDir"`
	Head             string                       `json:"head"`
	HeadSeen         time.Time                    `json:"headSeen,omitempty"`
	HeadRepo         string                       `json:"headRepo,omitempty"`
	HeadStale        bool                         `json:"headStale,omitempty"`
	HeadVerifiedAt   time.Time                    `json:"headVerifiedAt,omitempty"`
	State            string                       `json:"state"`
	ClosedAt         time.Time                    `json:"closedAt,omitempty"`
	CICleanupDone    bool                         `json:"ciCleanupDone,omitempty"`
	Watching         bool                         `json:"watching"`
	Created          time.Time                    `json:"created"`
	Checked          time.Time                    `json:"checked,omitempty"`
	CI               string                       `json:"ci,omitempty"`
	CIReason         string                       `json:"ciReason,omitempty"`
	BotReviews       []BotReview                  `json:"botReviews,omitempty"`
	Checks           []CheckSummary               `json:"checks"`
	CIKey            string                       `json:"ciKey,omitempty"`
	Failures         int                          `json:"consecutiveFailures"`
	CIPaused         bool                         `json:"ciPaused"`
	PauseReason      string                       `json:"pauseReason,omitempty"`
	Error            string                       `json:"error,omitempty"`
	Round            int                          `json:"reviewRound"`
	RoundHead        string                       `json:"reviewHead,omitempty"`
	RoundStarted     time.Time                    `json:"roundStarted,omitempty"`
	ReviewPublishing string                       `json:"reviewPublishing,omitempty"`
	ReviewerMode     string                       `json:"reviewerMode,omitempty"`
	ReviewerOverride string                       `json:"reviewerOverride,omitempty"`
	Reviewer         string                       `json:"reviewerSession,omitempty"`
	ReviewerName     string                       `json:"reviewerName,omitempty"`
	Verdict          string                       `json:"verdict,omitempty"`
	Summary          string                       `json:"summary,omitempty"`
	ReviewID         int64                        `json:"reviewId,omitempty"`
	CommentDecisions map[string]Decision          `json:"commentDecisions,omitempty"`
	Seen             map[string]string            `json:"seen,omitempty"`
	Notices          []Notice                     `json:"notifications,omitempty"`
	Overlaps         []Overlap                    `json:"overlaps,omitempty"`
	PendingDrops     map[string]int               `json:"pendingDroppedComments,omitempty"`
	// Assignments remain available after the active round resets.
	LastReviewer      string              `json:"lastReviewer,omitempty"`
	LastReviewHead    string              `json:"lastReviewHead,omitempty"`
	ReviewAssignments map[string][]string `json:"reviewAssignments,omitempty"`
}

// HasPendingVerdict reports a saved review awaiting confirmation on head.
func (r Record) HasPendingVerdict(head string) bool {
	for p := r.PendingVerdict; p != nil; p = p.Next {
		if p.Head == head {
			return true
		}
	}
	return false
}

// RepositoryFacts records the last default-branch observation from GitHub.
type RepositoryFacts struct {
	MergeTrialCI  bool   `json:"mergeTrialCI,omitempty"`
	NoPushCI      bool   `json:"noPushCI,omitempty"`
	DefaultBranch string `json:"defaultBranch"`
	Head          string `json:"head"`
	Verdict       string `json:"verdict"`
	// CheckedAt is the query start, so older responses cannot replace newer facts.
	CheckedAt time.Time `json:"checkedAt"`
}

// MergeTrial keeps only frozen action inputs and progress. PR eligibility stays
// in Records and is checked again before publication.
type MergeTrialChange struct {
	Key     string `json:"key"`
	Head    string `json:"head"`
	Message string `json:"message,omitempty"`
}

// Overlap is what another open change in the same repository shares with this
// one. Head and OtherHead identify the compared pair; git runs again only
// when one of them moves.
type Overlap struct {
	ChangeID string `json:"changeId"`
	// Title names a change that has no PR number, such as a local change.
	Title     string   `json:"title,omitempty"`
	Number    int      `json:"number,omitempty"`
	Files     []string `json:"files,omitempty"`
	Conflicts []string `json:"conflicts,omitempty"`
	Head      string   `json:"head"`
	OtherHead string   `json:"otherHead"`
	MainHead  string   `json:"mainHead,omitempty"`
	Error     string   `json:"error,omitempty"`
}

// SharedOverlaps keeps the overlaps that name a shared or conflicting file.
func SharedOverlaps(all []Overlap) []Overlap {
	var out []Overlap
	for _, o := range all {
		if len(o.Files) > 0 || len(o.Conflicts) > 0 {
			out = append(out, o)
		}
	}
	return out
}

type MergeTrialFailure struct {
	Change MergeTrialChange `json:"change"`
	Reason string           `json:"reason"`
	Logs   string           `json:"logs,omitempty"`
}
type MergeTrialStack struct {
	Branch string `json:"branch"`
	Head   string `json:"head,omitempty"`
	Count  int    `json:"count"`
	Phase  string `json:"phase"`
	Reason string `json:"reason,omitempty"`
	Logs   string `json:"logs,omitempty"`
}
type MergeTrial struct {
	// TODO: legacy/shim remove later — retain the session ID from saved advisory reviews until their batches resume or are canceled; remove after all pre-upgrade batches are gone.
	LegacyReview *struct {
		Session string `json:"session,omitempty"`
	} `json:"review,omitempty"`

	RootReport    string             `json:"rootReport,omitempty"`
	Notified      bool               `json:"notified,omitempty"`
	Notifier      string             `json:"notifier,omitempty"`
	Bisect        bool               `json:"bisect,omitempty"`
	StackChanges  []MergeTrialChange `json:"stackChanges,omitempty"`
	RootBranch    string             `json:"rootBranch,omitempty"`
	OriginalHead  string             `json:"originalHead,omitempty"`
	Stacks        []MergeTrialStack  `json:"stacks,omitempty"`
	Failure       *MergeTrialFailure `json:"failure,omitempty"`
	MaxParallel   int                `json:"maxParallel,omitempty"`
	Branch        string             `json:"branch"`
	Base          string             `json:"base"`
	DefaultBranch string             `json:"defaultBranch"`
	Head          string             `json:"head,omitempty"`
	Phase         string             `json:"phase"`
	Reason        string             `json:"reason,omitempty"`
	Changes       []MergeTrialChange `json:"changes"`
	Created       time.Time          `json:"created"`
	Logs          string             `json:"logs,omitempty"`
}

type State struct {
	MergePauses     map[string]string                    `json:"mergePauses,omitempty"`
	MergeTrials     map[string]*MergeTrial               `json:"mergeTrials,omitempty"`
	NextMergeTrial  uint64                               `json:"nextMergeTrial,omitempty"`
	IssueWork       map[string]IssueWork                 `json:"issueWork,omitempty"`
	RepositoryFacts map[string]RepositoryFacts           `json:"repositoryFacts,omitempty"`
	Policies        map[string]changepolicy.Policy       `json:"policies,omitempty"`
	Responses       map[string]map[string]CachedResponse `json:"httpResponses,omitempty"`
	RateLimit       RateLimit                            `json:"-"`
	RateLimits      map[string]RateLimit                 `json:"rateLimits,omitempty"`
	// TODO: legacy/shim remove later — retains unscoped saved pauses until their original deadline.
	LegacyPause time.Time            `json:"legacyQuotaPause,omitempty"`
	Version     int                  `json:"version"`
	Records     map[string]*Record   `json:"records"`
	Reviewers   map[string]Reviewer  `json:"reviewers"`
	NoiseRules  map[string]NoiseRule `json:"noiseRules"`
	// CommentSessions maps "kind:id" GitHub comment and review IDs to the Dyna
	// session that posted them. Dyna records each comment it posts instead of
	// writing a marker into the comment body.
	CommentSessions map[string]string `json:"commentSessions,omitempty"`
}

type Polling struct {
	// Error describes incomplete ancillary work, not a failed head confirmation.
	Error     string `json:"error,omitempty"`
	CIVersion string `json:"ciVersion,omitempty"`
	// PullVersion identifies metadata and policy from the last complete poll.
	PullVersion     string    `json:"pullVersion,omitempty"`
	PullHead        string    `json:"pullHead,omitempty"`
	FullChecked     time.Time `json:"fullChecked,omitempty"`
	Next            time.Time `json:"next,omitempty"`
	LastChange      time.Time `json:"lastChange,omitempty"`
	IntervalMinutes int       `json:"intervalMinutes,omitempty"`
	Fingerprint     string    `json:"fingerprint,omitempty"`
}

type CachedResponse struct {
	ETag string          `json:"etag"`
	Body json.RawMessage `json:"body"`
	Next string          `json:"next,omitempty"`
}

type RateLimitScope struct {
	Host     string `json:"host"`
	Account  string `json:"account"`
	Resource string `json:"resource"`
}

type RateLimit struct {
	ObservedAt time.Time      `json:"observedAt,omitzero"`
	PauseScope RateLimitScope `json:"pauseScope"`

	Remaining         *int      `json:"remaining,omitempty"`
	Reset             time.Time `json:"reset,omitempty"`
	PauseUntil        time.Time `json:"pauseUntil,omitempty"`
	Reason            string    `json:"reason,omitempty"`
	SecondaryFailures int       `json:"secondaryFailures,omitempty"`
}

type NoiseRule struct {
	ID            string    `json:"id"`
	Host          string    `json:"host"`
	Repo          string    `json:"repo"`
	Bot           string    `json:"bot"`
	Template      string    `json:"template"`
	Reason        string    `json:"reason"`
	SourceURL     string    `json:"sourceUrl"`
	SourceComment string    `json:"sourceComment"`
	LearnedBy     string    `json:"learnedBy,omitempty"`
	Created       time.Time `json:"created"`
	Hits          int       `json:"hits"`
	LastMatched   time.Time `json:"lastMatched,omitempty"`
}

type Decision struct {
	Action string `json:"action"`
	Reason string `json:"reason"`
}

type CheckSummary struct {
	Node                  string `json:"node,omitempty"`
	DurationMs            int64  `json:"durationMs,omitempty"`
	ID                    int64  `json:"id,omitempty"`
	Completed             string `json:"completed,omitempty"`
	FailureExcerpt        string `json:"failureExcerpt,omitempty"`
	FailureExcerptChecked bool   `json:"failureExcerptChecked,omitempty"`
	Name                  string `json:"name"`
	State                 string `json:"state"`
	Status                string `json:"status"`
	Conclusion            string `json:"conclusion"`
	URL                   string `json:"url"`
	CommentURL            string `json:"commentUrl,omitempty"`
}

type BotReview struct {
	Bot      string `json:"bot"`
	State    string `json:"state"`
	Findings int    `json:"findings"`
	URL      string `json:"url"`
}
