package engine

import (
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

func TestFailStaleStarts(t *testing.T) {
	s, room, first, ctx := setup(t)
	s.now = func() time.Time { return time.Unix(1061, 0) }
	second, e := s.SessionStart(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionStartRequest{Room: room, Name: "fresh", Profile: "test", Goal: "goal"})
	if e != nil {
		t.Fatal(e)
	}
	if err := s.reconciler.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	s.reconciler.Wait()
	old, err := s.store.Session(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		id     contract.BoxID
		state  contract.BoxState
		reason string
	}{
		{old.Box, contract.BoxFailed, "no hello within 60 s"},
		{second.Box, contract.BoxStarting, ""},
	} {
		b, err := s.store.Box(ctx, tc.id)
		if err != nil || b.State != tc.state || b.Reason != tc.reason {
			t.Fatalf("box = %+v, error = %v; want %s %q", b, err, tc.state, tc.reason)
		}
	}
	events, err := s.store.Events(ctx, room, 0, 100, []contract.EventType{"box.changed"})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range events {
		change := event.Body.(contract.BoxChanged)
		if change.Box == old.Box && change.To == contract.BoxFailed {
			found = true
			if change.Cause != contract.CauseNoHello {
				t.Fatalf("cause = %q", change.Cause)
			}
		}
	}
	if !found {
		t.Fatal("missing failed box event")
	}
}
