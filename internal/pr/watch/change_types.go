package watch

import (
	"dyna-agent/internal/pr/state"
	"dyna-agent/internal/rolesession"
	"time"
)

type ReviewerSession struct {
	ID      string `json:"id"`
	Address string `json:"address"`
	Status  string `json:"status"`
}

type ReviewerHelper struct {
	ID      string `json:"id"`
	Address string `json:"address"`
	Profile string `json:"profile"`
	Status  string `json:"status"`
}

type ReviewCurrent struct {
	ChangeID  string    `json:"changeId,omitempty"`
	Kind      string    `json:"kind,omitempty"`
	PR        int       `json:"pr"`
	Title     string    `json:"title"`
	Head      string    `json:"head"`
	StartedAt time.Time `json:"startedAt"`
}

type ReviewQueued struct {
	GoalID    string `json:"goalId,omitempty"`
	Expedited bool   `json:"expedited,omitempty"`
	ChangeID  string `json:"changeId,omitempty"`
	Kind      string `json:"kind,omitempty"`
	PR        int    `json:"pr"`
	Title     string `json:"title"`
	Head      string `json:"head"`
}

type ReviewerSlotStatus struct {
	Session   *ReviewerSession `json:"session" tstype:"ReviewerSession | null,required"`
	Profile   string           `json:"profile"`
	Current   *ReviewCurrent   `json:"current" tstype:"ReviewCurrent | null,required"`
	Status    string           `json:"status"`
	LastError string           `json:"lastError"`
}

type ReviewerStatus struct {
	ExpeditedGoalID string               `json:"expeditedGoalId,omitempty"`
	Reviewers       []ReviewerSlotStatus `json:"reviewers" tstype:"ReviewerSlotStatus[] | null"`
	Helpers         []ReviewerHelper     `json:"helpers" tstype:"ReviewerHelper[] | null"`

	Enabled bool `json:"enabled"`
	// Profile is the profile the pool uses. ProfileCause is the typed reason the
	// pinned profile could not be used, and stays empty when the pool runs the
	// pin or chooses automatically. ProfileReason gives the same cause in words.
	Profile       string               `json:"profile"`
	ProfileSource string               `json:"profileSource"`
	ProfileCause  rolesession.Cause    `json:"profileCause,omitempty"`
	ProfileReason string               `json:"profileReason"`
	Queue         []ReviewQueued       `json:"queue" tstype:"ReviewQueued[] | null"`
	LastError     string               `json:"lastError"`
	Recent        []state.RecentReview `json:"recent" tstype:"RecentReview[] | null"`
}

// MergeQueueItem is a view of current facts. It is never saved as queue state.
type MergeQueueItem struct {
	GoalID                   string            `json:"goalId,omitempty"`
	Expedited                bool              `json:"expedited,omitempty"`
	MissingRequiredReviewers []string          `json:"missingRequiredReviewers,omitempty"`
	Title                    string            `json:"title,omitempty"`
	Author                   string            `json:"author,omitempty"`
	RoomID                   string            `json:"roomId"`
	ChangeID                 string            `json:"changeId"`
	URL                      string            `json:"url"`
	Head                     string            `json:"head"`
	State                    string            `json:"state"`
	Reason                   string            `json:"reason,omitempty"`
	ReadyAt                  time.Time         `json:"readyAt,omitempty"`
	Main                     *MainCI           `json:"main,omitempty"`
	FailedSuite              string            `json:"failedSuite,omitempty"`
	Excerpt                  string            `json:"excerpt,omitempty"`
	Trial                    *MergeTrialStatus `json:"trial,omitempty"`
	Overlaps                 []state.Overlap   `json:"overlaps,omitempty"`
	Dependency               *MergeDependency  `json:"dependency,omitempty"`
}

// MergeDependency identifies a change that blocks this queue row. Clients use
// its number and title for display; ChangeID remains the stable machine key.
type MergeDependency struct {
	ChangeID string `json:"changeId"`
	Number   int    `json:"number,omitempty"`
	Title    string `json:"title,omitempty"`
}

type MergeTrialStatus struct {
	Branch      string                  `json:"branch"`
	Head        string                  `json:"head,omitempty"`
	Phase       state.MergeTrialPhase   `json:"phase"`
	Reason      string                  `json:"reason,omitempty"`
	Batch       []string                `json:"batch" tstype:"string[] | null"`
	MaxParallel int                     `json:"maxParallel,omitempty"`
	Stacks      []state.MergeTrialStack `json:"stacks,omitempty"`
}

// MainWait is why the queue waits on the default branch, as facts. The commit
// is MainCI.Head; a screen shortens it and words the kind.
type MainWait struct {
	Kind MainWaitKind `json:"kind"`
	// Base is the default branch name, when the probe knows it.
	Base string `json:"base,omitempty"`
}

// MainCI is the saved CI result of the repository's default branch. State is
// passed, failed, pending or paused. Every item carries it, so a screen reads one field.
type MainCI struct {
	State string    `json:"state"`
	Head  string    `json:"head"`
	Wait  *MainWait `json:"wait,omitempty"`
}

type MergeControlStatus struct {
	LocalCIChecks   map[string][]CheckSummary `json:"localCIChecks,omitempty" tstype:"LocalCIChecks"`
	ExpeditedGoalID string                    `json:"expeditedGoalId,omitempty"`
	RoomID          string                    `json:"roomId"`
	Paused          bool                      `json:"paused"`
	Reason          string                    `json:"reason,omitempty"`
	Queue           []MergeQueueItem          `json:"queue" tstype:"MergeQueueItem[] | null"`
	Trials          []MergeTrialStatus        `json:"trials" tstype:"MergeTrialStatus[] | null"`
}

// MergeHistoryEntry is one trial, publish, or decision of the merge service.
type MergeHistoryEntry struct {
	Kind    string    `json:"kind"`    // "trial", "publish", or "decision"
	Outcome string    `json:"outcome"` // "passed", "failed", "published", or "recorded"
	Repo    string    `json:"repo"`
	Branch  string    `json:"branch"`
	Head    string    `json:"head"` // tested commit; the new main commit for a publish
	Changes []string  `json:"changes" tstype:"string[] | null"`
	Detail  string    `json:"detail,omitempty"` // failure evidence or a decision line
	At      time.Time `json:"at"`
}

// ReviewBlockKind says why review waits, so a view can tell a routine wait
// (head-refresh, which the watcher clears on its next good refresh) from a
// block that someone must clear.
type ReviewBlockKind string

// Review block kinds.
const (
	ReviewBlockKindRoomPaused      ReviewBlockKind = "room-paused"
	ReviewBlockKindDraft           ReviewBlockKind = "draft"
	ReviewBlockKindHeadRefresh     ReviewBlockKind = "head-refresh"
	ReviewBlockKindReviewerStopped ReviewBlockKind = "reviewer-stopped"
	ReviewBlockKindCIFailed        ReviewBlockKind = "ci-failed"
)

// MainWaitKind names why the queue waits on the default branch's CI. The web
// writes the words from the kind; the service never formats a sentence.
type MainWaitKind string

// Main wait kinds.
const (
	MainWaitKindNoPushCI MainWaitKind = "no-push-ci"
	MainWaitKindNoHead   MainWaitKind = "no-head"
	MainWaitKindPassed   MainWaitKind = "passed"
	MainWaitKindFailed   MainWaitKind = "failed"
	MainWaitKindPending  MainWaitKind = "pending"
	MainWaitKindPaused   MainWaitKind = "paused"
)
