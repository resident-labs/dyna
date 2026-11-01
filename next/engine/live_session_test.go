package engine

import (
	"testing"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/live"
)

// A store read begun before close cannot revive the closed live session.
func TestClosedSessionRejectsStaleReadAndHandle(t *testing.T) {
	s, _, id, ctx := setup(t)
	row, open, err := s.store.OpenSession(ctx, id)
	if err != nil || !open {
		t.Fatalf("open session: %v, %v", open, err)
	}
	handle, ok := s.live.CreateSession(row.ID)
	if !ok {
		t.Fatal("open session refused")
	}
	stopped := make(chan struct{})
	if !handle.Lock() {
		t.Fatal("open handle refused")
	}
	handle.SetChannel(live.NewChannel(func() { close(stopped) }, stopped))
	handle.SetComputer(row.Room, contract.ComputerRunning)
	watch := handle.WatchComputer()
	handle.Unlock()
	if _, problem := s.SessionClose(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionCloseRequest{Session: id, Discard: true}); problem != nil {
		t.Fatal(problem)
	}
	passLifecycle(t, s, ctx) // remove the box
	passLifecycle(t, s, ctx) // commit the session close
	select {
	case <-stopped:
	default:
		t.Fatal("close did not stop the box channel")
	}
	select {
	case _, ok := <-watch.Events():
		if ok {
			t.Fatal("closed computer watcher delivered a frame")
		}
	default:
		t.Fatal("close did not close the computer watcher")
	}
	caller := contract.Principal{Kind: contract.PrincipalSession, Session: id}
	if _, problem := s.TurnActivity(ctx, caller, contract.TurnActivityRequest{Label: "stale"}); problem == nil || problem.Code != contract.ErrNotFound {
		t.Fatalf("closed activity: %v", problem)
	}
	if _, _, ok := s.lockSession(row.ID); ok {
		t.Fatal("stale channel writer recreated the session")
	}
	if handle.Lock() {
		handle.Unlock()
		t.Fatal("stale channel handle remained usable")
	}
	if _, problem := s.BoxChannel(ctx, caller); problem == nil || problem.Code != contract.ErrNotFound {
		t.Fatalf("closed channel: %v", problem)
	}
	body := contract.TurnActivity{Session: id, Label: "stale"}
	if handle.PublishActivity(row.Room, body, contract.Event{Body: body}) {
		t.Fatal("stale handle remained usable")
	}
	if _, ok := s.live.LookupSession(id); ok {
		t.Fatal("closed session remains in live state")
	}
}
