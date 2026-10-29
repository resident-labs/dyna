// Package state owns the persisted PR schema and reads atomic snapshots.
package state

import (
	"dyna-agent/internal/changepolicy"
	"dyna-agent/internal/room"
	"encoding/json"
	"time"
)

// RecentReview is one row of a room's recent reviewer results, for status.
type RecentReview struct {
	ChangeID string        `json:"changeId,omitempty"`
	PR       int           `json:"pr"`
	Verdict  ReviewVerdict `json:"verdict"`
	At       time.Time     `json:"at"`
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
	CommentID string `json:"commentId,omitempty"`
	NeedsRoot bool   `json:"needsRoot,omitempty"`
	System    bool   `json:"system,omitempty"`
	// Type is the registered PR event type this notice posts as. A notice
	// stored before the typed events existed leaves it empty, and delivery
	// then reads the type from its kind and the record.
	Type room.PRType `json:"type,omitempty"`
	// Merge is the typed merge event this notice posts. A merge notice carries
	// it; every other notice leaves it nil.
	Merge *room.MergeEvent `json:"merge,omitempty"`
	// Review is the typed review result this notice posts. A verdict notice
	// carries it; every other notice leaves it nil.
	Review *room.ReviewEvent `json:"review,omitempty"`
	// Summary is the short facts line of the event: the failed check names or
	// the commenter's action. It never holds raw log lines.
	Summary string `json:"summary,omitempty"`
	// Commenter is the GitHub login of a comment or review author.
	Commenter string `json:"commenter,omitempty"`
	// Verdict is the reviewer verdict of a verdict notice.
	Verdict string `json:"verdict,omitempty"`
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
	// DoneAt is when delivery finished. PruneDoneNotices removes the notice
	// DoneNoticeKeep after it; a notice from before this field is stamped when
	// the pass first sees it.
	DoneAt time.Time `json:"doneAt,omitempty"`
	Error  string    `json:"error,omitempty"`
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

// FindingSeverity says how strongly a finding asks for a change.
type FindingSeverity string

// Finding severities.
const (
	FindingBlocking  FindingSeverity = "blocking"
	FindingShouldFix FindingSeverity = "should-fix"
	FindingNit       FindingSeverity = "nit"
)

type Finding struct {
	ID       string          `json:"id"`
	Severity FindingSeverity `json:"severity"`
	File     string          `json:"file"`
	Line     int             `json:"line"`
	Why      string          `json:"why"`
	Fix      string          `json:"fix"`
	Patch    string          `json:"patch,omitempty"`
	Replies  []FindingReply  `json:"replies"`
}

// ReviewVerdict is the typed result of one reviewer turn. A reviewer turn ends
// with exactly one of these; the engine rejects any other value.
type ReviewVerdict string

// Review verdicts.
const (
	// VerdictApprove approves the reviewed head.
	VerdictApprove ReviewVerdict = "approve"
	// VerdictChanges asks the author for changes, with typed findings.
	VerdictChanges ReviewVerdict = "changes"
	// VerdictApproveFast approves a change for a fast landing at the reviewer's
	// own discretion.
	VerdictApproveFast ReviewVerdict = "approve-fast"
	// VerdictStopEarly ends the review without a verdict on the change.
	VerdictStopEarly ReviewVerdict = "stop-early"
)

// StopReason is why a reviewer stopped before it judged the change.
type StopReason string

// Stop reasons, in the order a reviewer usually meets them.
const (
	StopSuperseded StopReason = "superseded"
	StopDuplicate  StopReason = "duplicate"
	StopEmpty      StopReason = "empty"
	StopTooLarge   StopReason = "too-large"
)

// LocalCIConfigState is the local CI configuration a repository has at a head.
// A view reports one of these known states, so a client words it itself and
// never shows a Git or YAML message.
type LocalCIConfigState string

// Local CI configuration states.
const (
	// LocalCIConfigNone means the head and its base both define local CI, or
	// that the question does not apply to this change.
	LocalCIConfigNone LocalCIConfigState = ""
	// LocalCIConfigNotConfigured means the base defines no local CI, so the
	// repository uses hosted CI only.
	LocalCIConfigNotConfigured LocalCIConfigState = "not-configured"
	// LocalCIConfigHeadMissing means the head removed a definition that its
	// base still requires.
	LocalCIConfigHeadMissing LocalCIConfigState = "head-missing"
)

// known reports whether a stored value is a state that a client renders.
func (c LocalCIConfigState) known() bool {
	return c == LocalCIConfigNone || c == LocalCIConfigNotConfigured || c == LocalCIConfigHeadMissing
}

// FastLand records whether an approve-fast result uses direct publication.
// The stored Boolean also preserves the meaning of older review events.
type FastLand struct {
	LowRisk bool `json:"lowRisk"`
}

// ReviewResult is one reviewer turn's typed result, as recorded through the
// engine. Head and ReviewID say which submission it is; the rest is what the
// review decided.
type ReviewResult struct {
	Verdict    ReviewVerdict `json:"verdict"`
	Findings   []Finding     `json:"findings,omitempty"`
	StopReason StopReason    `json:"stopReason,omitempty"`
	FastLand   *FastLand     `json:"fastLand,omitempty"`
	Summary    string        `json:"summary,omitempty"`
	Head       string        `json:"head,omitempty"`
	ReviewID   int64         `json:"reviewId,omitempty"`
}

type ReviewRound struct {
	Reviewer   string        `json:"reviewer,omitempty"`
	ReviewID   int64         `json:"reviewId,omitempty"`
	Round      int           `json:"round"`
	Head       string        `json:"head"`
	Verdict    ReviewVerdict `json:"verdict"`
	Summary    string        `json:"summary"`
	Findings   []Finding     `json:"findings"`
	StopReason StopReason    `json:"stopReason,omitempty"`
	FastLand   *FastLand     `json:"fastLand,omitempty"`
	At         time.Time     `json:"at"`
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
	Next       *PendingVerdict `json:"next,omitempty"`
	Actor      string          `json:"actor"`
	Head       string          `json:"head"`
	Verdict    ReviewVerdict   `json:"verdict"`
	Summary    string          `json:"summary"`
	ReviewID   int64           `json:"reviewId,omitempty"`
	Findings   []Finding       `json:"findings"`
	StopReason StopReason      `json:"stopReason,omitempty"`
	FastLand   *FastLand       `json:"fastLand,omitempty"`
}

// Controller names who controls the workflow run that opened a change: the
// session that started that run, or ControllerRoot when root or the owner
// started it. A later run agent of the same controller may take the change's
// author role over once the run's earlier author closed (issue D-252). No
// session id equals ControllerRoot, because session ids start "ses_".
type Controller string

// ControllerRoot is the controller of a run that the room's root session or
// the owner started.
const ControllerRoot Controller = "root"

// PRState is the GitHub state a tracked change has, as Dyna records it.
type PRState string

// PR states.
const (
	PROpen   PRState = "open"
	PRClosed PRState = "closed"
	PRMerged PRState = "merged"
)

// CIState is the CI result a tracked change has at its head.
type CIState string

// CI states. The zero value means no CI result is recorded yet.
const (
	CIPending CIState = "pending"
	CIPassed  CIState = "passed"
	CIFailed  CIState = "failed"
	CIAbsent  CIState = "absent"
)

// ReviewerMode is how a change's reviewer runs.
type ReviewerMode string

// Reviewer modes. The zero value means no reviewer mode is recorded.
const ReviewerModeSession ReviewerMode = "session"

// Hold keeps a PR out of merge in Dyna's own state. GitHub draft status alone
// does not hold it, because any ready-for-review call lifts that draft.
type Hold struct {
	Reason string    `json:"reason"`
	By     string    `json:"by,omitempty"` // session that set the hold, empty for the owner
	At     time.Time `json:"at"`
}

type Record struct {
	PendingVerdict *PendingVerdict `json:"pendingVerdict,omitempty"`
	// ReleaseReviewersAfterReplay holds closed reviewers until their saved verdicts replay.
	ReleaseReviewersAfterReplay []string           `json:"releaseReviewersAfterReplay,omitempty"`
	MergeTrialFailure           *MergeTrialFailure `json:"mergeTrialFailure,omitempty"`
	IsDraft                     bool               `json:"isDraft,omitempty"`
	Hold                        *Hold              `json:"hold,omitempty"`
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
	CIQueueWait      map[string]string     `json:"ciQueueWait,omitempty"`
	PolicyOverride   changepolicy.Override `json:"policyOverride"`
	Landing          *Landing              `json:"landing,omitempty"`
	ChangeID         string                `json:"changeId,omitempty"`
	GoalID           string                `json:"goalId,omitempty"` // goal of the same room that this change serves
	Kind             string                `json:"kind,omitempty"`
	RepoPath         string                `json:"repoPath,omitempty"`
	BaseRef          string                `json:"baseRef,omitempty"`
	BaseRetargetSeen time.Time             `json:"baseRetargetSeen,omitempty"` // a base change that waits for the parent's merge
	Notes            AuthorNotes           `json:"notes"`
	Category         string                `json:"category,omitempty"`
	Dependencies     []string              `json:"dependencies,omitempty"`
	// Fixes holds the issues a merge closes: room issues (D-N) and GitHub issues (#N).
	Fixes []IssueRef `json:"fixes,omitempty"`
	// MergeCommit is the commit that merged the PR, recorded when it closed the fixes.
	MergeCommit       string            `json:"mergeCommit,omitempty"`
	Findings          []Finding         `json:"findings"`
	RequiredReviewers []string          `json:"requiredReviewers,omitempty"`
	ReviewRounds      []ReviewRound     `json:"reviewRounds"`
	CITestPolicy      string            `json:"ciTestPolicy,omitempty"`
	CITestReportNote  string            `json:"ciTestReportNote,omitempty"`
	CINoJUnitWarned   bool              `json:"ciNoJUnitWarned,omitempty"`
	CITestHead        string            `json:"ciTestHead,omitempty"`
	CITestErrors      map[string]string `json:"ciTestErrors,omitempty"`
	CITestFailures    map[string]string `json:"ciTestFailures,omitempty"`
	CITestRuns        map[string]string `json:"ciTestRuns,omitempty"`
	CITestRetries     map[string]int    `json:"ciTestRetries,omitempty"`

	CIStaleCleanupHead string `json:"ciStaleCleanupHead,omitempty"`

	Polling       Polling   `json:"polling"`
	RateLimit     RateLimit `json:"rateLimit"`
	QuotaAccount  string    `json:"quotaAccount,omitempty"`
	Key           string    `json:"key"`
	Host          string    `json:"host"`
	Repo          string    `json:"repo"`
	Number        int       `json:"number"`
	URL           string    `json:"url"`
	Title         string    `json:"title"`
	RoomID        string    `json:"roomId"`
	Author        string    `json:"authorSession"`
	FormerAuthors []string  `json:"formerAuthors,omitempty"`
	// Controller is the controller of the run whose agent opened this change:
	// the session that started that run, or ControllerRoot. Records stored
	// before controllers were recorded, and changes no run opened, have none,
	// so no takeover applies to them.
	Controller       Controller                   `json:"controller,omitempty"`
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
	State            PRState                      `json:"state"`
	ClosedAt         time.Time                    `json:"closedAt,omitempty"`
	CICleanupDone    bool                         `json:"ciCleanupDone,omitempty"`
	Watching         bool                         `json:"watching"`
	Created          time.Time                    `json:"created"`
	Checked          time.Time                    `json:"checked,omitempty"`
	CI               CIState                      `json:"ci,omitempty"`
	CIReason         string                       `json:"ciReason,omitempty"`
	LocalCIConfig    LocalCIConfigState           `json:"localCIConfig,omitempty"`
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
	ReviewerMode     ReviewerMode                 `json:"reviewerMode,omitempty"`
	ReviewerOverride string                       `json:"reviewerOverride,omitempty"`
	Reviewer         string                       `json:"reviewerSession,omitempty"`
	ReviewerName     string                       `json:"reviewerName,omitempty"`
	Verdict          ReviewVerdict                `json:"verdict,omitempty"`
	Summary          string                       `json:"summary,omitempty"`
	ReviewID         int64                        `json:"reviewId,omitempty"`
	StopReason       StopReason                   `json:"stopReason,omitempty"`
	FastLand         *FastLand                    `json:"fastLand,omitempty"`
	CommentTrust     string                       `json:"commentTrust,omitempty"`
	OwnerLogin       string                       `json:"ownerLogin,omitempty"`
	HeldComments     map[string]HeldComment       `json:"heldComments,omitempty"`
	AdmittedComments map[string]string            `json:"admittedComments,omitempty"`
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

// MergeTrialPhase is the persisted execution phase of a batch or prefix.
type MergeTrialPhase string

// Merge trial phases retain their stored JSON spellings.
const (
	MergeTrialPhaseQueued       MergeTrialPhase = "queued"
	MergeTrialPhaseBuilding     MergeTrialPhase = "building"
	MergeTrialPhaseRunning      MergeTrialPhase = "running"
	MergeTrialPhaseReviewing    MergeTrialPhase = "reviewing"
	MergeTrialPhasePassed       MergeTrialPhase = "passed"
	MergeTrialPhaseFailed       MergeTrialPhase = "failed"
	MergeTrialPhaseStacking     MergeTrialPhase = "stacking"
	MergeTrialPhasePublishing   MergeTrialPhase = "publishing"
	MergeTrialPhasePublished    MergeTrialPhase = "published"
	MergeTrialPhaseAwaitingRoot MergeTrialPhase = "awaiting-root"
)

// MergePauseCause identifies why merging is paused, independently of its text.
type MergePauseCause string

const (
	MergePauseCauseManual      MergePauseCause = "manual"
	MergePauseCauseBatchFailed MergePauseCause = "batch-failed"
)

// MergeBatchFailedReason is the display text for a failed-batch pause.
const MergeBatchFailedReason = "batch trial failed; merging paused for recovery"

// MergePause keeps the machine-readable cause next to the display reason.
type MergePause struct {
	Cause  MergePauseCause `json:"cause"`
	Reason string          `json:"reason"`
}

func (p *MergePause) UnmarshalJSON(data []byte) error {
	var legacyReason string
	if err := json.Unmarshal(data, &legacyReason); err == nil {
		*p = legacyMergePause(legacyReason)
		return nil
	}
	type pause MergePause
	var saved pause
	if err := json.Unmarshal(data, &saved); err != nil {
		return err
	}
	*p = MergePause(saved)
	return nil
}

// TODO: legacy/shim remove later — map pauses saved before causes were stored.
func legacyMergePause(reason string) MergePause {
	cause := MergePauseCauseManual
	if reason == MergeBatchFailedReason {
		cause = MergePauseCauseBatchFailed
	}
	return MergePause{Cause: cause, Reason: reason}
}

type MergeTrialStack struct {
	Branch string          `json:"branch"`
	Head   string          `json:"head,omitempty"`
	Count  int             `json:"count"`
	Phase  MergeTrialPhase `json:"phase"`
	Reason string          `json:"reason,omitempty"`
	Logs   string          `json:"logs,omitempty"`
}
type MergeTrial struct {
	// TODO: legacy/shim remove later — retain the session ID from saved advisory reviews until their batches resume or are canceled; remove after all pre-upgrade batches are gone.
	LegacyReview *struct {
		Session string `json:"session,omitempty"`
	} `json:"review,omitempty"`

	// FailReason and FailChecks are the typed facts of the failure that paused
	// this batch. They are saved with the pause, so a delivery retry, a restart
	// or a fresh tick posts the same cause and failed checks to root.
	FailReason    string             `json:"failReason,omitempty"`
	FailChecks    []string           `json:"failChecks,omitempty"`
	RootReport    string             `json:"rootReport,omitempty"`
	Notified      bool               `json:"notified,omitempty"`
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
	Phase         MergeTrialPhase    `json:"phase"`
	Reason        string             `json:"reason,omitempty"`
	Changes       []MergeTrialChange `json:"changes"`
	Created       time.Time          `json:"created"`
	Logs          string             `json:"logs,omitempty"`

	// LocalCIWait records a trial head that waits for a required local CI run
	// which no machine has started. The merge queue uses it to bound that wait:
	// one retry, then one root alert. It is nil while local CI runs, has a
	// complete result, or is not required for the head.
	LocalCIWait *MergeTrialLocalCIWait `json:"localCiWait,omitempty"`
}

// MergeTrialLocalCIWait is the merge queue's bound on a trial head that waits
// for local CI that never started.
type MergeTrialLocalCIWait struct {
	// Since is when this unstarted wait was first seen, and it moves to the
	// retry time once the retry ran, so the alert gets its own full window.
	Since time.Time `json:"since"`
	// Retried reports that the one bounded scheduling retry already ran.
	Retried bool `json:"retried,omitempty"`
	// Alerted reports that root was already alerted about this wait.
	Alerted bool `json:"alerted,omitempty"`
}

type State struct {
	MergePauses     map[string]MergePause                `json:"mergePauses,omitempty"`
	MergeTrials     map[string]*MergeTrial               `json:"mergeTrials,omitempty"`
	NextMergeTrial  uint64                               `json:"nextMergeTrial,omitempty"`
	IssueWork       map[string]IssueWork                 `json:"issueWork,omitempty"`
	RepositoryFacts map[string]RepositoryFacts           `json:"repositoryFacts,omitempty"`
	Policies        map[string]changepolicy.Policy       `json:"policies,omitempty"`
	Responses       map[string]map[string]CachedResponse `json:"httpResponses,omitempty"`
	RateLimit       RateLimit                            `json:"-"`
	RateLimits      map[string]RateLimit                 `json:"rateLimits,omitempty"`
	// TODO: legacy/shim remove later — retains unscoped saved pauses until their original deadline.
	LegacyPause time.Time            `json:"-"`
	Version     int                  `json:"version"`
	Records     map[string]*Record   `json:"records"`
	Reviewers   map[string]Reviewer  `json:"reviewers"`
	NoiseRules  map[string]NoiseRule `json:"noiseRules"`
	// CommentSessions maps "kind:id" GitHub comment and review IDs to the Dyna
	// session that posted them. Dyna records each comment it posts instead of
	// writing a marker into the comment body.
	CommentSessions map[string]string `json:"commentSessions,omitempty"`

	// unchanged identifies shared snapshot rows that UpdateRecords never exposed
	// to its callback. The writer compares their pointer identity instead of
	// walking every nested review and notice again. It is never persisted.
	unchanged map[string]*Record
}

type Polling struct {
	// Error describes incomplete ancillary work, not a failed head confirmation.
	Error     string `json:"error,omitempty"`
	CIVersion string `json:"ciVersion,omitempty"`
	// LocalCI identifies the saved local CI result this PR has already read. A
	// manual rerun writes only Dyna's records, so a new result needs a full poll
	// that a quiet pull would otherwise skip.
	LocalCI string `json:"localCI,omitempty"`
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

// HeldComment is stored in the PR record in SQLite until the owner admits it.
type HeldComment struct {
	ID        string `json:"id"`
	Commenter string `json:"commenter"`
	Body      string `json:"body"`
	URL       string `json:"url"`
	Version   string `json:"version"`
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
