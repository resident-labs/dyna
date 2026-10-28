package watch

import (
	"context"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/providerfailure"
	"dyna-agent/internal/subagent"
	"strings"
	"testing"
	"time"
)

func TestReconcileKeepsBusyTurnsAndFillsOldestNeed(t *testing.T) {
	e, keys := readyReviewPool(t, 4)
	st := dispatchReviewPool(t, e)
	facts, err := e.s.ReviewFacts(st)
	if err != nil {
		t.Fatal(err)
	}
	if len(facts.Busy) != 3 || len(facts.Queue(e.author.RoomID)) != 1 || facts.Queue(e.author.RoomID)[0].Key != keys[3] {
		t.Fatalf("busy=%d queue=%+v", len(facts.Busy), facts.Queue(e.author.RoomID))
	}
	ids := map[string]string{}
	for key, live := range facts.Busy {
		ids[key] = live.Info.ID
	}
	// Elapsed time and a profile change do not invalidate a live turn.
	e.clock = e.clock.Add(24 * time.Hour)
	if err := e.s.SetReviewerProfile(e.author.RoomID, "mock"); err != nil {
		t.Fatal(err)
	}
	st = dispatchReviewPool(t, e)
	for key, id := range ids {
		if st.Records[key].Reviewer != id || e.reviewers[id].Status != subagent.StatusRunning {
			t.Fatal("live turn was replaced")
		}
	}
	if len(e.launches) != 3 {
		t.Fatal("busy turns lost their capacity")
	}
	// A current-head verdict frees capacity, and the session closes once its
	// turn ends; the verdict itself ended this turn (D-411).
	if err := e.s.Store.update(func(st *State) error { st.Records[keys[0]].Verdict = "changes"; return nil }); err != nil {
		t.Fatal(err)
	}
	e.endReviewerTurn(ids[keys[0]])
	st = dispatchReviewPool(t, e)
	if st.Records[keys[3]].Reviewer == "" || e.reviewers[ids[keys[0]]].Status != subagent.StatusClosed {
		t.Fatal("verdict did not free capacity")
	}
}

func TestReconcileCapacityFailureUsesFreshReviewer(t *testing.T) {
	for _, tc := range []struct {
		message string
		reason  providerfailure.Reason
	}{
		{"quota exceeded", providerfailure.Quota},
		{"Selected model is at capacity", providerfailure.Capacity},
	} {
		t.Run(tc.message, func(t *testing.T) {
			e, keys := readyReviewPool(t, 1)
			e.s.ReviewerProfiles = func() ([]profile.Profile, error) {
				return []profile.Profile{{Name: "judge", Taste: 9, Cost: 9, MaxConcurrent: 1}, {Name: "mock", Taste: 1, Cost: 1, MaxConcurrent: 1}}, nil
			}
			st := dispatchReviewPool(t, e)
			old := st.Records[keys[0]].Reviewer
			original := e.s.ReviewerInfo
			e.s.ReviewerInfo = func(id string) (subagent.Info, subagent.Turn, error) {
				info, turn, err := original(id)
				if id == old && info.Status != subagent.StatusClosed {
					info.Status = subagent.StatusIdle
					info.CurrentTurn = 0
					turn.Status = subagent.TurnError
					turn.Error = tc.message
					turn.FailureReason = tc.reason
					turn.Ended = e.clock
				}
				return info, turn, err
			}
			st = dispatchReviewPool(t, e)
			r := st.Records[keys[0]]
			if r.Reviewer == old || r.RoundHead != r.Head || e.reviewers[r.Reviewer].Profile != "mock" || e.reviewers[old].Status != subagent.StatusClosed {
				t.Fatalf("failed profile retained review: %+v", r)
			}
			if !strings.Contains(e.launches[len(e.launches)-1], "/mock/") {
				t.Fatal("no fallback assignment")
			}
		})
	}
}

func TestRestartReconcilesEveryNeededHead(t *testing.T) {
	e, keys := readyReviewPool(t, 4)
	st := dispatchReviewPool(t, e)
	old := map[string]bool{}
	for _, r := range st.Records {
		if r.Reviewer != "" {
			old[r.Reviewer] = true
		}
	}
	if err := e.s.Store.update(func(st *State) error {
		r := st.Records[keys[0]]
		r.Verdict = "changes"
		r.Head = strings.Repeat("b", 40)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	e.setPullHead(17, strings.Repeat("b", 40))
	if err := e.s.RestartReviewer(context.Background(), e.author.RoomID); err != nil {
		t.Fatal(err)
	}
	st, _ = e.s.Store.Read()
	for id := range old {
		if e.reviewers[id].Status != subagent.StatusClosed {
			t.Fatal("restart retained old session")
		}
	}
	facts, err := e.s.ReviewFacts(st)
	if err != nil {
		t.Fatal(err)
	}
	if len(facts.Busy) != 3 || len(facts.Queue(e.author.RoomID)) != 1 {
		t.Fatalf("restart lost work: busy=%d queue=%d", len(facts.Busy), len(facts.Queue(e.author.RoomID)))
	}
	if r := st.Records[keys[0]]; r.RoundHead != r.Head || old[r.Reviewer] {
		t.Fatal("restart did not review current head")
	}
}

func TestReviewOffClosesNonNeededSessions(t *testing.T) {
	e, keys := readyReviewPool(t, 1)
	st := dispatchReviewPool(t, e)
	old := st.Records[keys[0]].Reviewer
	if err := e.s.SetReviewEnabled(e.author.RoomID, false); err != nil {
		t.Fatal(err)
	}
	// The disabled room's reviewer finished its turn, so the pool closes it.
	e.endReviewerTurn(old)
	st = dispatchReviewPool(t, e)
	facts, err := e.s.ReviewFacts(st)
	if err != nil {
		t.Fatal(err)
	}
	if e.reviewers[old].Status != subagent.StatusClosed || len(facts.Need) != 0 || len(facts.Busy) != 0 {
		t.Fatal("disabled review retained capacity")
	}
}
