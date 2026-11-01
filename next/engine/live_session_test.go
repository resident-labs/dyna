package engine

import (
	"testing"

	"github.com/resident-labs/dyna/contract"
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
	if _, problem := s.SessionClose(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionCloseRequest{Session: id, Discard: true}); problem != nil {
		t.Fatal(problem)
	}
	passLifecycle(t, s, ctx) // remove the box
	passLifecycle(t, s, ctx) // commit the session close
	if _, ok := s.live.CreateSession(row.ID); ok {
		t.Fatal("stale open read recreated the session")
	}
	body := contract.TurnActivity{Session: id, Label: "stale"}
	if handle.PublishActivity(row.Room, body, contract.Event{Body: body}) {
		t.Fatal("stale handle remained usable")
	}
	if _, ok := s.live.LookupSession(id); ok {
		t.Fatal("closed session remains in live state")
	}
}
