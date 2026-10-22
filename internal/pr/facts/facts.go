// Package facts derives review and merge eligibility from PR and live-session facts.
package facts

import (
	"dyna-agent/internal/pr/state"
	"dyna-agent/internal/subagent"
	"time"
)

// GitHub is a current remote observation, not a saved workflow state.
// A nil Mergeable means GitHub has not finished computing mergeability.
type GitHub struct {
	Head          string
	Base          string
	DefaultBranch string
	IsDraft       bool
	Mergeable     *bool
	CIHead        string
	CI            string
	ChecksAt      time.Time
}

func HeadVerified(r state.Record) bool { return r.Head != "" && !r.HeadStale && r.Error == "" }

// HeadCurrent compares a fresh head with the verified head used for an action.
func HeadCurrent(r state.Record, head string) bool {
	return HeadVerified(r) && head == r.Head
}

func ApprovedVerdict(verdict string) bool {
	return verdict == "approve" || verdict == "approve-with-nits"
}
func ApprovedAtHead(r state.Record) bool {
	return r.RoundHead == r.Head && ApprovedVerdict(r.Verdict)
}
func NeedsReview(r state.Record) bool {
	return r.Watching && r.State == "open" && !r.IsDraft && HeadVerified(r) && r.CI != "failed" &&
		!r.HasPendingVerdict(r.Head) && (r.Verdict == "" || r.RoundHead != r.Head)
}
func Busy(r state.Record, info subagent.Info, turn subagent.Turn) bool {
	return NeedsReview(r) && r.RoundHead == r.Head && r.Reviewer == info.ID && r.Reviewer != r.Author &&
		info.Role == "reviewer" && info.RoomID == r.RoomID && info.Status == subagent.StatusRunning &&
		info.CurrentTurn > 0 && turn.N == info.CurrentTurn && turn.Status == subagent.TurnRunning && !turn.Done()
}

// OpenOwnerFinding reports whether an owner comment still waits for an answer.
// A comment stays open until the author replies fixed or wont-fix, or the owner resolves it.
func OpenOwnerFinding(r state.Record) bool {
	for _, f := range r.Findings {
		if f.Severity != "owner" {
			continue
		}
		if n := len(f.Replies); n == 0 || f.Replies[n-1].Disposition == "question" {
			return true
		}
	}
	return false
}
func Ready(r state.Record, g GitHub) bool {
	return len(MissingRequiredReviewers(r)) == 0 && !OpenOwnerFinding(r) && (r.MergeTrialFailure == nil || r.MergeTrialFailure.Change.Head != r.Head) && r.Watching && r.State == "open" && HeadCurrent(r, g.Head) && ApprovedAtHead(r) && r.CI == "passed" &&
		g.CIHead == r.Head && g.CI == "passed" && !g.IsDraft &&
		g.Mergeable != nil && *g.Mergeable && g.DefaultBranch != "" && g.Base == g.DefaultBranch && g.Base == r.BaseRef
}
func ReadyAt(r state.Record, g GitHub) time.Time {
	at := g.ChecksAt
	for _, round := range r.ReviewRounds {
		if round.Head == r.Head && ApprovedVerdict(round.Verdict) && round.At.After(at) {
			at = round.At
		}
	}
	if at.IsZero() {
		at = r.Created
	}
	return at
}

// FromRecord projects the last confirmed remote facts for display and notice suppression.
// Actions that refresh GitHub must use their fresh observation instead.
func FromRecord(r state.Record) GitHub {
	return GitHub{Head: r.Head, Base: r.BaseRef, DefaultBranch: r.DefaultBranch, IsDraft: r.IsDraft, Mergeable: r.Mergeable, CIHead: r.Head, CI: r.CI, ChecksAt: r.ChecksAt}
}
