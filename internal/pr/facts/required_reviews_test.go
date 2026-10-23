package facts

import (
	"dyna-agent/internal/pr/state"
	"reflect"
	"testing"
)

func TestRequiredReviewsUseEachReviewersLatestExactHeadVerdict(t *testing.T) {
	r := state.Record{Watching: true, State: "open", Head: "current", RoundHead: "current", Verdict: "approve", CI: "passed", BaseRef: "main", RequiredReviewers: []string{"architecture", "safety"}}
	yes := true
	g := GitHub{Head: r.Head, CIHead: r.Head, CI: "passed", Base: "main", DefaultBranch: "main", Mergeable: &yes}
	require := func(want []string) {
		t.Helper()
		if got := MissingRequiredReviewers(r); !reflect.DeepEqual(got, want) {
			t.Fatalf("missing = %v, want %v", got, want)
		}
		if Ready(r, g) != (len(want) == 0) {
			t.Fatalf("ready = %v with missing %v", Ready(r, g), want)
		}
	}
	require([]string{"architecture", "safety"})
	// A pool approval and an old required approval cannot satisfy the gate.
	r.ReviewRounds = []state.ReviewRound{{Reviewer: "pool", Head: r.Head, Verdict: "approve"}, {Reviewer: "architecture", Head: "old", Verdict: "approve"}}
	require([]string{"architecture", "safety"})
	r.ReviewRounds = append(r.ReviewRounds, state.ReviewRound{Reviewer: "architecture", Head: r.Head, Verdict: "approve-with-nits"})
	require([]string{"safety"})
	r.ReviewRounds = append(r.ReviewRounds, state.ReviewRound{Reviewer: "safety", Head: r.Head, Verdict: "approve"})
	require(nil)
	// A later question or change replaces this reviewer's earlier approval.
	for _, verdict := range []state.ReviewVerdict{"changes", "question"} {
		r.ReviewRounds = append(r.ReviewRounds, state.ReviewRound{Reviewer: "safety", Head: r.Head, Verdict: verdict})
		require([]string{"safety"})
	}
	r.Head = "new"
	if got := MissingRequiredReviewers(r); !reflect.DeepEqual(got, []string{"architecture", "safety"}) {
		t.Fatalf("push retained approvals: %v", got)
	}
	r.Head = ""
	r.ReviewRounds = append(r.ReviewRounds, state.ReviewRound{Reviewer: "architecture", Verdict: "approve"})
	if len(MissingRequiredReviewers(r)) != 2 {
		t.Fatal("empty head counted as approval")
	}
	r.RequiredReviewers = nil
	if MissingRequiredReviewers(r) != nil {
		t.Fatal("requirements appeared on an unchanged PR")
	}
}
