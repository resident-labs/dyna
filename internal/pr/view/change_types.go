package view

import (
	"dyna-agent/internal/pr/state"
	"dyna-agent/internal/pr/watch"
	"time"
)

type ChangeView struct {
	GoalID                   string   `json:"goalId,omitempty"`
	RequiredReviewers        []string `json:"requiredReviewers,omitempty"`
	MissingRequiredReviewers []string `json:"missingRequiredReviewers,omitempty"`
	IsDraft                  bool     `json:"isDraft"`
	DetailVersion            string   `json:"detailVersion"`
	HasPolicyOverride        bool     `json:"hasPolicyOverride,omitempty"`
	ChangeID                 string   `json:"changeId,omitempty"`
	Kind                     string   `json:"kind,omitempty"`
	Branch                   string   `json:"branch,omitempty"`
	BaseRef                  string   `json:"baseRef,omitempty"`
	Base                     string   `json:"base,omitempty"`
	ReviewState              string   `json:"reviewState,omitempty"`
	QueuePosition            *int     `json:"queuePosition" tstype:"number | null,required"`
	// UpstreamPR is true when maintainers merge this change: its base is a parent
	// repository of the room, so Dyna tracks it but never queues or merges it.
	UpstreamPR    bool   `json:"upstreamPr,omitempty"`
	ReviewBlocked string `json:"reviewBlocked,omitempty"`

	Key                 string               `json:"key,omitempty"`
	Repo                string               `json:"repo,omitempty"`
	Number              int                  `json:"number,omitempty"`
	URL                 string               `json:"url,omitempty"`
	Title               string               `json:"title,omitempty"`
	RoomID              string               `json:"roomId,omitempty"`
	AuthorSession       string               `json:"authorSession,omitempty"`
	ReviewerSession     string               `json:"reviewerSession,omitempty"`
	HeldCommentCount    int                  `json:"heldCommentCount"`
	ReviewPublishing    string               `json:"reviewPublishing,omitempty"`
	ReviewerMode        string               `json:"reviewerMode,omitempty"`
	ReviewerName        string               `json:"reviewerName,omitempty"`
	State               string               `json:"state,omitempty"`
	Watching            bool                 `json:"watching,omitempty"`
	Status              string               `json:"status,omitempty"`
	CI                  string               `json:"ci,omitempty"`
	CITestReportNote    string               `json:"ciTestReportNote,omitempty"`
	CIReason            string               `json:"ciReason,omitempty"`
	BotReviews          []state.BotReview    `json:"botReviews" tstype:"BotReview[] | null"`
	Checks              []watch.CheckSummary `json:"checks" tstype:"CheckSummary[] | null"`
	ConsecutiveFailures int                  `json:"consecutiveFailures,omitempty"`
	CIPaused            bool                 `json:"ciPaused,omitempty"`
	PauseReason         string               `json:"pauseReason,omitempty"`
	Error               string               `json:"error,omitempty"`
	ReviewRound         int                  `json:"reviewRound,omitempty"`
	ReviewHead          string               `json:"reviewHead,omitempty"`
	Verdict             state.ReviewVerdict  `json:"verdict,omitempty"`
	ReviewAt            time.Time            `json:"reviewAt,omitzero"`
	FindingCount        int                  `json:"findingCount,omitempty"`
	HasReviewSummary    bool                 `json:"hasReviewSummary,omitempty"`
	ReviewURL           string               `json:"reviewUrl,omitempty"`
	Head                string               `json:"head,omitempty"`
	Created             time.Time            `json:"created,omitempty"`
	Checked             time.Time            `json:"checked,omitempty"`
	FilteredComments    int                  `json:"filteredComments,omitempty"`
	Overlaps            []state.Overlap      `json:"overlaps,omitempty"`
	// Fixes lists the issues that this change closes when it merges.
	Fixes       []state.IssueRef `json:"fixes,omitempty"`
	MergeCommit string           `json:"mergeCommit,omitempty"`

	// LocalCIConfig is one of the known local CI configuration states, so a
	// client words it itself.
	LocalCIConfig state.LocalCIConfigState `json:"localCIConfig,omitempty"`
}

type HeldPRComment struct {
	Key               string `json:"key"`
	RoomID            string `json:"roomId"`
	Number            int    `json:"number"`
	state.HeldComment `tstype:",extends"`
}

// CIBudgetSettings contains the room's pull request run and test limits.
// A nil no-JUnit warning setting uses the default, which is true.
type CIBudgetSettings struct {
	CITimeBudget     string            `json:"ciTimeBudget"`
	CIBudgetExempt   []string          `json:"ciBudgetExempt" tstype:"string[] | null"`
	CIWarnAfter      string            `json:"ciWarnAfter"`
	CIFailAfter      string            `json:"ciFailAfter"`
	CITestMax        string            `json:"ciTestMax"`
	CITestAllowNotes map[string]string `json:"ciTestAllowNotes" tstype:"StringMapOrNull"`
	CITestAllow      []string          `json:"ciTestAllow" tstype:"string[] | null"`
	CINoJUnitWarning *bool             `json:"ciNoJUnitWarning" tstype:"boolean | null,required"`
}

type PRNoiseList struct {
	Repositories []string          `json:"repositories" tstype:"string[] | null"`
	Rules        []watch.NoiseRule `json:"rules" tstype:"NoiseRule[] | null"`
	Warning      string            `json:"warning,omitempty"`
}

type PRConfig struct {
	MergeMaxParallel int    `json:"mergeMaxParallel"`
	GitHubAccess     string `json:"githubAccess"`
	ReviewPublishing string `json:"reviewPublishing"`
	ReviewerProfile  string `json:"reviewerProfile"`
	ReviewerSession  string `json:"reviewerSession"`
}
