package watch

import (
	"sort"
	"testing"
	"time"

	"dyna-agent/internal/pr/state"
	"dyna-agent/internal/subagent"
)

// ReviewFacts reads the reviewers of open PRs and of PRs closed within the
// last hour, so the pool can release them. Older closed PRs keep their
// reviewer IDs forever; reading every one of those sessions on each status
// call was the engine's largest CPU cost.
func TestReviewFactsSkipsReviewersOfLongClosedPRs(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	var read []string
	s := &Service{Now: func() time.Time { return now }, ReviewerInfo: func(id string) (subagent.Info, subagent.Turn, error) {
		read = append(read, id)
		return subagent.Info{Meta: subagent.Meta{ID: id, Role: "reviewer"}}, subagent.Turn{}, nil
	}}
	st := State{Version: 1, Records: map[string]*state.Record{
		"open":       {Key: "open", RoomID: "r", State: "open", Reviewer: "ses_open"},
		"just":       {Key: "just", RoomID: "r", State: "merged", ClosedAt: now.Add(-time.Minute), Reviewer: "ses_just"},
		"old-merged": {Key: "old-merged", RoomID: "r", State: "merged", ClosedAt: now.Add(-2 * time.Hour), Reviewer: "ses_old", LastReviewer: "ses_old_last"},
		"old-closed": {Key: "old-closed", RoomID: "r", State: "closed", ClosedAt: now.Add(-48 * time.Hour), Reviewer: "ses_closed"},
	}}
	facts, err := s.ReviewFacts(st)
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(read)
	if len(read) != 2 || read[0] != "ses_just" || read[1] != "ses_open" {
		t.Fatalf("read reviewer sessions %v, want only ses_just and ses_open", read)
	}
	if _, ok := facts.Sessions["ses_just"]; !ok {
		t.Fatalf("reviewer of a just-merged PR missing from facts: %v", facts.Sessions)
	}
}
