package watch

import (
	"context"
	"testing"

	"dyna-agent/internal/pr/state"
	"dyna-agent/internal/subagent"
)

func TestCloseReleasesReviewerAndPreservesVerdict(t *testing.T) {
	for _, verdict := range []string{"", "approve"} {
		t.Run(verdict, func(t *testing.T) {
			e := newEnv(t)
			r := e.track()
			const reviewer = "reviewer-to-close"
			if err := e.s.Store.update(func(st *State) error {
				live := st.Records[r.Key]
				live.Reviewer, live.ReviewerOverride, live.RoundHead, live.Verdict = reviewer, reviewer, "head", state.ReviewVerdict(verdict)
				if verdict == "" {
					live.RequiredReviewers = []string{reviewer}
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			result, err := e.s.CloseSession(context.Background(), subagent.Info{Meta: subagent.Meta{ID: reviewer, RoomID: r.RoomID}}, subagent.Info{})
			if err != nil {
				t.Fatal(err)
			}
			got := e.record()
			if got.Reviewer != "" || got.ReviewerOverride != "" || len(got.RequiredReviewers) != 0 || len(result) != 1 {
				t.Fatalf("unreleased roles: %+v; %v", got, result)
			}
			if got.Verdict != state.ReviewVerdict(verdict) || got.LastReviewer != reviewer || verdict == "" && got.RoundHead != "" || verdict != "" && got.RoundHead != "head" {
				t.Fatalf("review provenance: %+v", got)
			}
		})
	}
}
