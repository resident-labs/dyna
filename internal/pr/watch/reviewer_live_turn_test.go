package watch

import (
	"testing"

	"dyna-agent/internal/profile"
	"dyna-agent/internal/subagent"
)

// D-411: a failed poll left an error on the record, so Busy no longer saw the
// running review. reconcileRoom then force-closed the reviewer mid-turn and
// stopped its verdict shell before the verdict was saved. A live turn must
// always keep its session; the pool closes the session once the turn ends.
func TestPoolKeepsReviewerWhoseTurnStillRuns(t *testing.T) {
	e, keys := readyReviewPool(t, 1)
	st := dispatchReviewPool(t, e)
	id := st.Records[keys[0]].Reviewer
	if id == "" {
		t.Fatal("no reviewer assigned")
	}
	if err := e.s.Store.update(func(st *State) error {
		st.Records[keys[0]].Error = "poll failed"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	st = dispatchReviewPool(t, e)
	if got := e.reviewers[id].Status; got != subagent.StatusRunning {
		t.Fatalf("pool closed a reviewer whose turn still runs: status %q", got)
	}
	if r := st.Records[keys[0]]; r.Reviewer != id {
		t.Fatalf("record lost its reviewer while the turn runs: %q", r.Reviewer)
	}

	// The record error clears, so the same reviewer finishes the round.
	if err := e.s.Store.update(func(st *State) error {
		st.Records[keys[0]].Error = ""
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	launches := len(e.launches)
	dispatchReviewPool(t, e)
	if got := e.reviewers[id].Status; got != subagent.StatusRunning || len(e.launches) != launches {
		t.Fatalf("healthy round restarted: status %q launches %d then %d", got, launches, len(e.launches))
	}

	// The turn ends without a verdict, so the pool closes the session and
	// reassigns the review at once.
	e.endReviewerTurn(id)
	st = dispatchReviewPool(t, e)
	if got := e.reviewers[id].Status; got != subagent.StatusClosed {
		t.Fatal("finished reviewer stayed open")
	}
	if r := st.Records[keys[0]]; r.Reviewer == id || r.Reviewer == "" || len(e.launches) != launches+1 {
		t.Fatalf("missing verdict did not reassign at once: reviewer %q launches %d", r.Reviewer, len(e.launches))
	}
}

// A preserved live turn that Busy no longer sees (verdict saved, record error,
// moved head) still runs on its profile, so it must hold review capacity until
// its turn ends; otherwise the pool launches past the cap or double-reviews a
// moved head (D-411).
func TestPreservedLiveTurnHoldsCapacity(t *testing.T) {
	e, keys := readyReviewPool(t, 2)
	e.s.ReviewerProfiles = func() ([]profile.Profile, error) {
		return []profile.Profile{{Name: "judge", MaxConcurrent: 1}}, nil
	}
	st := dispatchReviewPool(t, e)
	first := *st.Records[keys[0]]
	if err := e.s.Store.update(func(st *State) error {
		st.Records[keys[0]].Verdict = "approve"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	st = dispatchReviewPool(t, e)
	if st.Records[keys[1]].Reviewer != "" || len(e.launches) != 1 {
		t.Fatal("non-busy live turn did not hold review capacity")
	}
	if e.reviewers[first.Reviewer].Status != subagent.StatusRunning {
		t.Fatal("verdict-holding reviewer closed before its turn ended")
	}
	e.endReviewerTurn(first.Reviewer)
	st = dispatchReviewPool(t, e)
	if st.Records[keys[1]].Reviewer == "" || len(e.launches) != 2 {
		t.Fatal("capacity did not free once the turn ended")
	}
}
