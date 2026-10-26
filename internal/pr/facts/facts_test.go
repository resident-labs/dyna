package facts

import (
	"dyna-agent/internal/pr/state"
	"dyna-agent/internal/subagent"
	"testing"
	"time"
)

func TestReadyUsesCurrentRemoteFacts(t *testing.T) {
	yes, no := true, false
	now := time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)
	r := state.Record{Watching: true, State: "open", Head: "abc", RoundHead: "abc", Verdict: "approve", CI: "passed", Created: now, BaseRef: "trunk"}
	g := GitHub{Head: "abc", CIHead: "abc", CI: "passed", Base: "trunk", DefaultBranch: "trunk", Mergeable: &yes, ChecksAt: now.Add(time.Second)}
	if !Ready(r, g, nil) {
		t.Fatal("approved default-branch PR is not ready")
	}
	for _, tc := range []struct {
		name   string
		change func(*state.Record, *GitHub)
	}{
		{"unverified", func(r *state.Record, g *GitHub) { r.HeadStale = true }},
		{"old approval", func(r *state.Record, g *GitHub) { r.RoundHead = "old" }},
		{"pushed", func(r *state.Record, g *GitHub) { g.Head = "new" }},
		{"old CI", func(r *state.Record, g *GitHub) { g.CIHead = "old" }},
		{"fresh pending", func(r *state.Record, g *GitHub) { g.CI = "pending" }},
		{"fresh failure", func(r *state.Record, g *GitHub) { g.CI = "failed" }},
		{"draft", func(r *state.Record, g *GitHub) { g.IsDraft = true }},
		{"unknown mergeability", func(r *state.Record, g *GitHub) { g.Mergeable = nil }},
		{"conflict", func(r *state.Record, g *GitHub) { g.Mergeable = &no }},
		{"child", func(r *state.Record, g *GitHub) { g.Base = "feature" }},
		{"unknown default", func(r *state.Record, g *GitHub) { g.DefaultBranch = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			copy, observed := r, g
			tc.change(&copy, &observed)
			if Ready(copy, observed, nil) {
				t.Fatal("unsafe READY")
			}
		})
	}
	r.ReviewRounds = []state.ReviewRound{{Head: r.Head, Verdict: "approve", At: now.Add(2 * time.Second)}, {Head: "old", Verdict: "approve", At: now.Add(time.Hour)}}
	if got := ReadyAt(r, g); got != now.Add(2*time.Second) {
		t.Fatalf("ready time = %s", got)
	}
	g.ChecksAt = now.Add(3 * time.Second)
	if got := ReadyAt(r, g); got != g.ChecksAt {
		t.Fatalf("check time = %s", got)
	}
}

func TestBusyRequiresLiveTurnOnNeededHead(t *testing.T) {
	r := state.Record{Watching: true, State: "open", Head: "abc", RoundHead: "abc", Reviewer: "reviewer", Author: "author", RoomID: "room"}
	info := subagent.Info{Meta: subagent.Meta{ID: "reviewer", Role: "reviewer", RoomID: "room"}, Status: subagent.StatusRunning, CurrentTurn: 1}
	turn := subagent.Turn{N: 1, Status: subagent.TurnRunning}
	if !Busy(r, info, turn) {
		t.Fatal("live reviewer is not busy")
	}
	for _, tc := range []struct {
		name   string
		change func(*state.Record, *subagent.Info, *subagent.Turn)
	}{
		{"draft", func(r *state.Record, i *subagent.Info, t *subagent.Turn) { r.IsDraft = true }},
		{"idle", func(r *state.Record, i *subagent.Info, t *subagent.Turn) { i.Status = subagent.StatusIdle }},
		{"verdict", func(r *state.Record, i *subagent.Info, t *subagent.Turn) { r.Verdict = "changes" }},
		{"moved head", func(r *state.Record, i *subagent.Info, t *subagent.Turn) { r.Head = "new" }},
		{"old turn", func(r *state.Record, i *subagent.Info, t *subagent.Turn) { t.N = 2 }},
		{"finished turn", func(r *state.Record, i *subagent.Info, t *subagent.Turn) { t.Status = subagent.TurnOK }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, b, c := r, info, turn
			tc.change(&a, &b, &c)
			if Busy(a, b, c) {
				t.Fatal("inactive reviewer retained capacity")
			}
		})
	}
}

func TestPendingReviewAtHeadChecksEveryReviewer(t *testing.T) {
	r := state.Record{Watching: true, State: "open", Head: "current", PendingVerdict: &state.PendingVerdict{Head: "old", Next: &state.PendingVerdict{Head: "current"}}}
	if NeedsReview(r) {
		t.Fatal("the second pending reviewer was ignored")
	}
}
