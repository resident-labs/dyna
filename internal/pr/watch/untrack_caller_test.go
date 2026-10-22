package watch

import (
	"context"
	"fmt"
	"slices"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/notify"
	"dyna-agent/internal/room"
	"dyna-agent/internal/subagent"
)

// Untrack saves the close, then retires the room's reviewers. That cleanup must
// act for the caller: a reviewer close with no actor fails, and nothing retries
// the cleanup, because the record is already saved as unwatched.
func TestUntrackCleanupActsForTheCaller(t *testing.T) {
	e := newEnv(t)
	e.s.Notify = notify.New(room.DefaultStore(), subagent.NewManager())
	r := e.track()
	reviewer := subagent.Info{Meta: subagent.Meta{ID: "ses_20261017-000003-cccccccc", RoomID: e.author.RoomID, Room: e.author.Room, Role: "reviewer", Name: "judge"}, Status: subagent.StatusRunning}
	e.reviewers[reviewer.ID] = reviewer
	if err := e.s.Store.update(func(st *State) error {
		live := st.Records[r.Key]
		live.Reviewer, live.ReviewerMode, live.RoundHead, live.Round = reviewer.ID, "session", live.Head, 1
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	caller := authority.Actor{Role: authority.Session, Session: e.author.ID, Room: e.author.RoomID}
	var closed []string
	var lostCaller error
	e.s.CloseReviewer = func(ctx context.Context, id string) error {
		if got := authority.Of(ctx); got != caller {
			lostCaller = fmt.Errorf("reviewer close acts for %+v, want the caller %+v", got, caller)
		}
		closed = append(closed, id)
		return nil
	}
	if err := e.s.Untrack(authority.WithActor(t.Context(), caller), r.Key); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(closed, reviewer.ID) {
		t.Fatalf("the reviewer was not retired: %v", closed)
	}
	if lostCaller != nil {
		t.Fatal(lostCaller)
	}
}
