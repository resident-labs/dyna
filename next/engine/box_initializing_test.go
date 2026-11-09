package engine

import (
	"context"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/lifecycle"
)

func TestHarnessStartupStaysStartingUntilChannelOpens(t *testing.T) {
	s, _, sid, ctx := setup(t)
	now := setupNow()
	s.now = func() time.Time { return now }
	s.reconciler = lifecycle.New(s.store, fakeBoxes{}, s, nil, s.event, s.now)
	caller := boxCaller(sid)
	if _, problem := s.BoxHello(ctx, caller, contract.BoxHelloRequest{WaitForChannel: true}); problem != nil {
		t.Fatal(problem)
	}
	session, err := s.store.Session(ctx, sid)
	if err != nil {
		t.Fatal(err)
	}
	current, err := s.store.Box(ctx, session.Box)
	if err != nil {
		t.Fatal(err)
	}
	if current.State != contract.BoxStarting || current.Reason != "initializing session" {
		t.Fatalf("hello falsely ready: %+v", current)
	}
	if allowed, err := s.store.CanDispatch(ctx, sid); err != nil || allowed {
		t.Fatalf("dispatch before harness ready: %v %v", allowed, err)
	}
	s.SetStartStatus(func(contract.BoxID) *contract.BoxStartStatus { return nil })
	listed, problem := s.SessionList(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionListRequest{Room: session.Room})
	if problem != nil {
		t.Fatal(problem)
	}
	found := false
	for _, item := range listed.Sessions {
		if item.ID == sid {
			found = item.Start != nil && item.Start.Step == contract.StartSession && item.Start.StartedAt.Equal(now)
		}
	}
	if !found {
		t.Fatal("session list did not retain loading-conversation status")
	}
	now = now.Add(118 * time.Second)
	passLifecycle(t, s, ctx)
	current, err = s.store.Box(ctx, session.Box)
	if err != nil {
		t.Fatal(err)
	}
	if current.State != contract.BoxStarting {
		t.Fatalf("slow resume was lost or killed: %+v", current)
	}
	channelCtx, cancel := context.WithCancel(ctx)
	messages, problem := s.BoxChannel(channelCtx, caller, "")
	if problem != nil {
		cancel()
		t.Fatal(problem)
	}
	defer func() {
		cancel()
		for range messages {
		}
	}()
	select {
	case message := <-messages:
		if _, ok := message.Body.(contract.TurnStart); !ok {
			t.Fatalf("start: %T", message.Body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ready harness did not receive turn")
	}
	current, err = s.store.Box(ctx, session.Box)
	if err != nil {
		t.Fatal(err)
	}
	if current.State != contract.BoxReady || current.Reason != "" {
		t.Fatalf("channel did not mark ready: %+v", current)
	}
}

func TestHarnessStartupDeadlineStillFailsAnAgentThatNeverConnects(t *testing.T) {
	s, _, sid, ctx := setup(t)
	now := setupNow()
	s.now = func() time.Time { return now }
	s.reconciler = lifecycle.New(s.store, fakeBoxes{}, s, nil, s.event, s.now)
	if _, problem := s.BoxHello(ctx, boxCaller(sid), contract.BoxHelloRequest{WaitForChannel: true}); problem != nil {
		t.Fatal(problem)
	}
	now = now.Add(10*time.Minute + time.Second)
	passLifecycle(t, s, ctx)
	session, err := s.store.Session(ctx, sid)
	if err != nil {
		t.Fatal(err)
	}
	current, err := s.store.Box(ctx, session.Box)
	if err != nil {
		t.Fatal(err)
	}
	if current.State != contract.BoxFailed || current.Reason != "session initialization exceeded 10 min" {
		t.Fatalf("unbounded or misleading startup: %+v", current)
	}
}
