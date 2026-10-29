package watch

import (
	"strings"
	"time"

	prfacts "dyna-agent/internal/pr/facts"
	"dyna-agent/internal/pr/state"
	"dyna-agent/internal/rolesession"
)

func retirePlan(facts ReviewFacts, roomID string, now time.Time) (busyIDs, retired map[string]bool, failures map[string]time.Time) {
	busyIDs = map[string]bool{}
	for id, live := range facts.Sessions {
		if live.Info.RoomID != roomID || !prfacts.TurnLive(live.Info, live.Turn) {
			continue
		}
		busyIDs[id] = true
	}
	failures = facts.State.Reviewers[roomID].FailedProfiles
	retired = map[string]bool{}
	for id, live := range facts.Sessions {
		if live.Info.RoomID != roomID || busyIDs[id] {
			continue
		}
		failures = rolesession.RecordFailure(failures, live.Info, live.Turn, now)
		retired[id] = true
	}
	return busyIDs, retired, failures
}

func applyRetire(current *State, roomID string, retired map[string]bool, failures map[string]time.Time) {
	cfg := current.Reviewers[roomID]
	cfg.FailedProfiles = failures
	cfg.LastError = ""
	current.Reviewers[roomID] = cfg
	for _, r := range current.Records {
		if r.RoomID != roomID {
			continue
		}
		for id := range retired {
			retireSessionReviewNotices(r, id, "review-reconciled")
		}
		if retired[r.Reviewer] && needsReview(*current, *r) {
			rememberReviewAssignment(r)
			r.Reviewer, r.ReviewerName, r.ReviewerMode, r.RoundHead = "", "", "", ""
			r.RoundStarted = time.Time{}
		}
		if strings.HasPrefix(r.PauseReason, "review paused:") {
			r.PauseReason = ""
		}
	}
}

func assignReview(current *State, r Record, sessionID string, cfg Reviewer) bool {
	live := current.Records[r.Key]
	if live == nil || current.Reviewers[r.RoomID].Profile != cfg.Profile || current.Reviewers[r.RoomID].ReviewPublishing != cfg.ReviewPublishing || live.Author == sessionID || live.Head != r.Head || !needsReview(*current, *live) || dependencyWaiting(*current, *live) {
		return false
	}
	rememberReviewAssignment(live)
	live.Round, live.RoundHead, live.RoundStarted = r.Round, r.Head, r.RoundStarted
	live.Verdict, live.Summary, live.ReviewID = "", "", 0
	live.StopReason, live.FastLand = "", nil
	live.Reviewer, live.ReviewerName, live.ReviewerMode = sessionID, r.ReviewerName, state.ReviewerModeSession
	live.ReviewPublishing = r.ReviewPublishing
	live.PauseReason = ""
	rememberReviewAssignment(live)
	return true
}
