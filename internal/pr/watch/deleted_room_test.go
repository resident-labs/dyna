package watch

import (
	"context"
	"os/exec"
	"testing"
)

func TestDeletedRoomKeepsHistoryWithoutBackgroundWork(t *testing.T) {
	e := newEscalationEnv(t)
	e.setCI("success", 1)
	r := e.testEnv.track()
	st, err := e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	before, err := e.s.ReviewFacts(st)
	if err != nil || len(before.Need) != 1 {
		t.Fatalf("review fixture: need=%d, err=%v", len(before.Need), err)
	}
	if _, err := e.rooms.Delete(e.rec.ID, nil); err != nil {
		t.Fatal(err)
	}
	facts, err := e.s.ReviewFacts(st)
	if err != nil {
		t.Fatalf("saved PR in deleted room blocked review facts: %v", err)
	}
	if !facts.Paused[r.RoomID] || len(facts.Need) != 0 {
		t.Errorf("deleted room created review need: paused=%v, need=%d", facts.Paused, len(facts.Need))
	}
	pauses := map[string]bool{}
	paused, err := e.s.roomPausedCached(r.RoomID, pauses)
	if err != nil || !paused {
		t.Errorf("polling deleted room: paused=%v, err=%v", paused, err)
	}
	if cached, ok := pauses[r.RoomID]; !ok || !cached {
		t.Errorf("deleted-room observation was not cached: %v", pauses)
	}
	calls := 0
	base := e.s.GH.run
	e.s.GH.run = func(ctx context.Context, cmd *exec.Cmd) error { calls++; return base(ctx, cmd) }
	if err := e.s.ConfirmHeads(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Errorf("deleted room made %d GitHub requests", calls)
	}
	if err := e.s.dispatchAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(e.launches) != 0 {
		t.Errorf("deleted room started %d reviewers", len(e.launches))
	}
	if err := e.s.Store.update(func(st *State) error { st.Records[r.Key].Verdict = "approve"; return nil }); err != nil {
		t.Fatal(err)
	}
	after, err := e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	if after.Records[r.Key] == nil {
		t.Error("deleted room lost saved PR history")
	}
}
