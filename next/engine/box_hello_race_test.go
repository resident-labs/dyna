package engine

import (
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/lifecycle"
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

// The other order: the hello is written first, then the host's create result. The agent keeps the reason and the budget
// its hello set, not the minute a box gets to say hello.
func TestACreateResultAfterTheHelloKeepsTheSessionStartupBudget(t *testing.T) {
	s, _, sid, ctx := setup(t)
	now := setupNow()
	s.now = func() time.Time { return now }
	s.reconciler = lifecycle.New(s.store, fakeBoxes{}, s, nil, s.event, s.now)
	session, err := s.store.Session(ctx, sid)
	if err != nil {
		t.Fatal(err)
	}
	before, err := s.store.Box(ctx, session.Box)
	if err != nil {
		t.Fatal(err)
	}
	if _, problem := s.BoxHello(ctx, boxCaller(sid), contract.BoxHelloRequest{WaitForChannel: true}); problem != nil {
		t.Fatal(problem)
	}
	greeted, err := s.store.Box(ctx, session.Box)
	if err != nil {
		t.Fatal(err)
	}
	event, err := s.engineEvent(session.Room, contract.BoxChanged{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.store.FinishCreate(ctx, before, greeted.Image, "", now.Add(time.Minute), 0, event, false); err != nil {
		t.Fatal(err)
	}
	after, err := s.store.Box(ctx, session.Box)
	if err != nil {
		t.Fatal(err)
	}
	if after.State != contract.BoxStarting || after.Reason != greeted.Reason {
		t.Fatalf("create result after hello: %s %q, hello left %s %q", after.State, after.Reason, greeted.State, greeted.Reason)
	}
	now = now.Add(2 * time.Minute)
	passLifecycle(t, s, ctx)
	if after, err = s.store.Box(ctx, session.Box); err != nil || after.State != contract.BoxStarting {
		t.Fatalf("two minutes into opening its session the box is %s: %v", after.State, err)
	}
}
