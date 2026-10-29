package watch

import (
	"reflect"
	"testing"
	"time"

	"dyna-agent/internal/pr/state"
)

func TestAssignReviewGuard(t *testing.T) {
	const (
		roomID   = "room"
		key      = "change"
		baseKey  = "base"
		head     = "head"
		author   = "author"
		reviewer = "reviewer"
	)
	started := time.Date(2026, time.October, 29, 15, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		mutate func(*State)
		want   bool
	}{
		{name: "head moved", mutate: func(st *State) {
			st.Records[key].Head = "moved-head"
		}},
		{name: "room pin changed", mutate: func(st *State) {
			cfg := st.Reviewers[roomID]
			cfg.Profile = "other"
			st.Reviewers[roomID] = cfg
		}},
		{name: "publishing mode changed", mutate: func(st *State) {
			cfg := st.Reviewers[roomID]
			cfg.ReviewPublishing = PublishingGitHub
			st.Reviewers[roomID] = cfg
		}},
		{name: "author is reviewer", mutate: func(st *State) {
			st.Records[key].Author = reviewer
		}},
		{name: "record no longer needs review", mutate: func(st *State) {
			st.Records[key].RoundHead = head
			st.Records[key].Verdict = state.VerdictApprove
		}},
		{name: "dependency waits", mutate: func(st *State) {
			st.Records[key].Dependencies = []string{baseKey}
			st.Records[baseKey] = &Record{Key: baseKey, State: state.PROpen}
		}},
		{name: "assignment applies", mutate: func(*State) {}, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Reviewer{Profile: "terra", ReviewPublishing: PublishingLocal}
			live := &Record{
				Key: key, RoomID: roomID, Author: author, Head: head,
				Watching: true, State: state.PROpen, CI: state.CIPassed,
				Round: 1, RoundHead: "previous-head", RoundStarted: started.Add(-time.Hour),
				Reviewer: "previous-reviewer", ReviewerName: "previous-name", ReviewerMode: state.ReviewerModeSession,
				Verdict: state.VerdictChanges, Summary: "previous summary", ReviewID: 12,
				StopReason: state.StopSuperseded, FastLand: &state.FastLand{LowRisk: true},
				ReviewPublishing: PublishingGitHub, PauseReason: "review paused: previous",
			}
			planned := Record{
				Key: key, RoomID: roomID, Author: author, Head: head,
				Round: 2, RoundHead: head, RoundStarted: started,
				ReviewerName: "next-reviewer", ReviewPublishing: PublishingLocal,
			}
			st := &State{
				Records:   map[string]*Record{key: live},
				Reviewers: map[string]Reviewer{roomID: cfg},
			}
			tt.mutate(st)
			before := *st.Records[key]
			if got := assignReview(st, planned, reviewer, cfg); got != tt.want {
				t.Fatalf("assignReview() = %v, want %v", got, tt.want)
			}
			if !tt.want {
				if got := *st.Records[key]; !reflect.DeepEqual(got, before) {
					t.Fatalf("rejected assignment changed the record: got %+v, want %+v", got, before)
				}
				return
			}
			want := before
			want.Round, want.RoundHead, want.RoundStarted = planned.Round, head, started
			want.Verdict, want.Summary, want.ReviewID = "", "", 0
			want.StopReason, want.FastLand = "", nil
			want.Reviewer, want.ReviewerName, want.ReviewerMode = reviewer, planned.ReviewerName, state.ReviewerModeSession
			want.ReviewPublishing = PublishingLocal
			want.PauseReason = ""
			want.LastReviewer, want.LastReviewHead = reviewer, head
			want.ReviewAssignments = map[string][]string{
				"previous-head": {"previous-reviewer"},
				head:            {reviewer},
			}
			if got := *st.Records[key]; !reflect.DeepEqual(got, want) {
				t.Fatalf("applied assignment: got %+v, want %+v", got, want)
			}
		})
	}
}
