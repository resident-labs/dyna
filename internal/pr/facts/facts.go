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

func ApprovedVerdict(verdict state.ReviewVerdict) bool {
	switch verdict {
	case state.VerdictApprove, state.VerdictApproveFast:
		return true
	// TODO: legacy/shim remove later — reviews recorded before the typed
	// results used this value; remove when retained review history ages out.
	case "approve-with-nits":
		return true
	}
	return false
}
func ApprovedAtHead(r state.Record) bool {
	return r.RoundHead == r.Head && ApprovedVerdict(r.Verdict)
}
func NeedsReview(r state.Record) bool {
	// A stable stop result (too-large, empty, duplicate, superseded) ends review
	// of this head. A new head, or dyna git pr reviewer --restart, resumes it.
	if r.RoundHead == r.Head && r.StopReason != "" {
		return false
	}
	return r.Watching && r.State == state.PROpen && !r.IsDraft && HeadVerified(r) && r.CI != state.CIFailed &&
		!r.HasPendingVerdict(r.Head) && (r.Verdict == "" || r.RoundHead != r.Head)
}
func Busy(r state.Record, info subagent.Info, turn subagent.Turn) bool {
	return NeedsReview(r) && r.RoundHead == r.Head && r.Reviewer == info.ID && r.Reviewer != r.Author &&
		info.Role == "reviewer" && info.RoomID == r.RoomID && info.Status == subagent.StatusRunning &&
		info.CurrentTurn > 0 && turn.N == info.CurrentTurn && turn.Status == subagent.TurnRunning && !turn.Done()
}

// TurnLive reports whether a reviewer session's own turn is queued or running,
// whatever its record says. A record condition that hides the review from Busy
// (a transient poll error, a failed CI read, a just-saved verdict) says nothing
// about the turn, which may still be recording that verdict, so a caller must
// not end the session while TurnLive holds (D-411).
func TurnLive(info subagent.Info, turn subagent.Turn) bool {
	return info.Role == "reviewer" && (info.Status == subagent.StatusRunning || info.Status == subagent.StatusQueued) &&
		info.CurrentTurn > 0 && turn.N == info.CurrentTurn && !turn.Done()
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

// UnmergedDependency returns the first recorded dependency that has not
// merged. A missing or closed-without-merging record also blocks readiness.
func UnmergedDependency(r state.Record, records map[string]*state.Record) (string, bool) {
	for _, id := range r.Dependencies {
		dependency := records[id]
		if dependency == nil {
			for _, candidate := range records {
				if candidate.ChangeID == id {
					dependency = candidate
					break
				}
			}
		}
		if dependency == nil || dependency.State != state.PRMerged {
			return id, true
		}
	}
	return "", false
}

func Ready(r state.Record, g GitHub, records map[string]*state.Record) bool {
	return ready(r, g, records, state.CIPassed)
}

// ReadyWithoutHeadCI reports a change whose head has no CI to read. A repository
// that publishes no CI for pull request heads cannot supply one, so waiting for
// it would never end: the merge trial runs the checks instead.
func ReadyWithoutHeadCI(r state.Record, g GitHub, records map[string]*state.Record) bool {
	return ready(r, g, records, state.CIAbsent)
}

// ready applies the one readiness rule to a head whose CI may be ci.
func ready(r state.Record, g GitHub, records map[string]*state.Record, ci state.CIState) bool {
	_, dependencyWaiting := UnmergedDependency(r, records)
	return !dependencyWaiting && len(MissingRequiredReviewers(r)) == 0 && !OpenOwnerFinding(r) && (r.MergeTrialFailure == nil || r.MergeTrialFailure.Change.Head != r.Head) && r.Watching && r.State == state.PROpen && HeadCurrent(r, g.Head) && ApprovedAtHead(r) && r.CI == ci &&
		g.CIHead == r.Head && g.CI == string(ci) && !g.IsDraft &&
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
	return GitHub{Head: r.Head, Base: r.BaseRef, DefaultBranch: r.DefaultBranch, IsDraft: r.IsDraft, Mergeable: r.Mergeable, CIHead: r.Head, CI: string(r.CI), ChecksAt: r.ChecksAt}
}
