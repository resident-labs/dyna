package engine

import (
	"testing"

	"github.com/resident-labs/dyna/contract"
)

// The agent's hello can read the box as creating just before the engine's own step to starting is written. The hello still
// counts: refusing it made the agent exit, and the box failed a minute later with "no hello within 60 s".
func TestHelloSurvivesTheBoxStartingBetweenItsReadAndItsWrite(t *testing.T) {
	s, _, sid, ctx := setup(t)
	session, err := s.store.Session(ctx, sid)
	if err != nil {
		t.Fatal(err)
	}
	current, err := s.store.Box(ctx, session.Box)
	if err != nil {
		t.Fatal(err)
	}
	stale := current
	for _, state := range []contract.BoxState{contract.BoxCreating, contract.BoxStarting} {
		if state != current.State {
			stale.State = state
		}
	}
	event, err := s.engineEvent(session.Room, contract.BoxChanged{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.store.BeginSessionStartup(ctx, stale, event); err != nil {
		t.Fatalf("hello with a stale read of %s while the box is %s: %v", stale.State, current.State, err)
	}
	after, err := s.store.Box(ctx, session.Box)
	if err != nil {
		t.Fatal(err)
	}
	if after.State != contract.BoxStarting || after.Reason != "initializing session" {
		t.Fatalf("box after hello: %+v", after)
	}
}
