package watch

import (
	"context"
	"strings"
	"testing"

	"dyna-agent/internal/profile"
	"dyna-agent/internal/subagent"
)

// Only a live turn on a needed current head may retain review capacity.
func TestReconcileReleasesNonBusyReviewersInOnePass(t *testing.T) {
	for _, kind := range []string{"idle without verdict", "verdict holder", "merged PR", "moved head", "dead process", "failed CI"} {
		t.Run(kind, func(t *testing.T) {
			e, keys := readyReviewPool(t, 2)
			e.s.ReviewerProfiles = func() ([]profile.Profile, error) { return []profile.Profile{{Name: "judge", MaxConcurrent: 1}}, nil }
			states := map[string]string{}
			turns := map[string]string{}
			e.s.ReviewerInfo = func(id string) (subagent.Info, subagent.Turn, error) {
				info := e.reviewers[id]
				if info.Status != subagent.StatusClosed {
					info.Status, info.CurrentTurn, info.Turns = subagent.StatusRunning, 1, 1
					if status := states[id]; status != "" {
						info.Status = status
						info.CurrentTurn = 0
					}
				}
				status := subagent.TurnRunning
				if turns[id] != "" {
					status = turns[id]
				}
				return info, subagent.Turn{N: 1, Status: status}, nil
			}
			closed := map[string]int{}
			e.s.CloseReviewer = func(_ context.Context, id string) error {
				closed[id]++
				info := e.reviewers[id]
				info.Status = subagent.StatusClosed
				e.reviewers[id] = info
				return nil
			}
			before := dispatchReviewPool(t, e)
			old := before.Records[keys[0]].Reviewer
			next := keys[0]
			if err := e.s.Store.update(func(st *State) error {
				r := st.Records[keys[0]]
				switch kind {
				case "idle without verdict":
					states[old] = subagent.StatusIdle
					turns[old] = subagent.TurnOK
				case "verdict holder":
					r.Verdict = "approve"
					next = keys[1]
				case "merged PR":
					r.State = "merged"
					r.Watching = false
					next = keys[1]
				case "moved head":
					r.Head = strings.Repeat("b", 40)
				case "dead process":
					states[old] = subagent.StatusIdle
				case "failed CI":
					r.CI = "failed"
					next = keys[1]
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if kind == "moved head" {
				e.setPullHead(17, strings.Repeat("b", 40))
			}
			after := dispatchReviewPool(t, e)
			if closed[old] != 1 {
				t.Fatalf("old reviewer retained capacity: closed=%d", closed[old])
			}
			current := after.Records[next]
			if current.Reviewer == "" || current.Reviewer == old || current.RoundHead != current.Head {
				t.Fatalf("free capacity did not review the oldest needed head: reviewer=%q old=%q head=%q round=%q", current.Reviewer, old, current.Head, current.RoundHead)
			}
			status, err := e.s.ReviewerStatus(e.author.RoomID)
			if err != nil {
				t.Fatal(err)
			}
			if len(status.Reviewers) != 1 || status.Reviewers[0].Session == nil || status.Reviewers[0].Session.ID != current.Reviewer {
				t.Fatalf("status and dispatch disagree about busy reviewers: %+v", status.Reviewers)
			}
		})
	}
}

// A push after the batch read must not start a review of the earlier head.
func TestReconcileSkipsHeadPushedAfterBatch(t *testing.T) {
	e, keys := readyReviewPool(t, 1)
	old := e.record().Head
	e.f.Pull.Head.SHA = strings.Repeat("b", 40)
	e.write()
	st := dispatchReviewPool(t, e)
	if len(e.launches) != 0 || st.Records[keys[0]].Reviewer != "" {
		t.Fatalf("started review of old head %s after push to %s", old, e.f.Pull.Head.SHA)
	}
	if err := e.s.ConfirmHeads(context.Background(), keys[0]); err != nil {
		t.Fatal(err)
	}
	st = dispatchReviewPool(t, e)
	r := st.Records[keys[0]]
	if len(e.launches) != 1 || r.Reviewer == "" || r.RoundHead != e.f.Pull.Head.SHA {
		t.Fatalf("confirmed new head was not reviewed: %+v", r)
	}
}
