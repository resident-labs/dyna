package watch

import (
	"context"
	"dyna-agent/internal/notify"
	"dyna-agent/internal/room"
	"dyna-agent/internal/subagent"
	"path/filepath"
	"testing"
)

func TestPausedRoomHasNoReviewNeed(t *testing.T) {
	e, keys := readyReviewPool(t, 1)
	rooms := &room.Store{Root: t.TempDir()}
	rec, err := rooms.Create(room.CreateInput{Name: "paused"})
	if err != nil {
		t.Fatal(err)
	}
	mgr := subagent.NewManager()
	mgr.Store = &subagent.Store{Root: filepath.Join(filepath.Dir(e.s.Store.Root), "sessions")}
	e.s.Notify = notify.New(rooms, mgr)
	e.author.RoomID, e.author.Room = rec.ID, rec.Name
	if err := e.s.Store.update(func(st *State) error { st.Records[keys[0]].RoomID = rec.ID; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := rooms.SetSessionsPaused(context.Background(), rec.ID, true); err != nil {
		t.Fatal(err)
	}
	st := dispatchReviewPool(t, e)
	facts, err := e.s.ReviewFacts(st)
	if err != nil {
		t.Fatal(err)
	}
	if len(facts.Need) != 0 || len(facts.Busy) != 0 || len(e.launches) != 0 {
		t.Fatalf("paused room created review work: need=%d busy=%d launches=%d", len(facts.Need), len(facts.Busy), len(e.launches))
	}
	if state, _, _ := facts.ReviewState(*st.Records[keys[0]]); state != "blocked" {
		t.Fatalf("paused state=%s", state)
	}
	if _, err := rooms.SetSessionsPaused(context.Background(), rec.ID, false); err != nil {
		t.Fatal(err)
	}
	st = dispatchReviewPool(t, e)
	if len(e.launches) != 1 || st.Records[keys[0]].Reviewer == "" {
		t.Fatal("resume did not restore review need")
	}
	id := st.Records[keys[0]].Reviewer
	if _, err := rooms.SetSessionsPaused(context.Background(), rec.ID, true); err != nil {
		t.Fatal(err)
	}
	// The paused room's reviewer finished its turn, so the pool closes it.
	e.endReviewerTurn(id)
	st = dispatchReviewPool(t, e)
	facts, err = e.s.ReviewFacts(st)
	if err != nil {
		t.Fatal(err)
	}
	if len(facts.Need) != 0 || len(facts.Busy) != 0 || e.reviewers[id].Status != subagent.StatusClosed || len(e.launches) != 1 {
		t.Fatal("pause retained or created review work")
	}
}
