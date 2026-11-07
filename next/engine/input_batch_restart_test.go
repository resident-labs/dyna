package engine

import (
	"context"
	"github.com/resident-labs/dyna/contract"
	"testing"
	"time"
)

func TestInputBatchEngineRestartKeepsOriginalDeadline(t *testing.T) {
	r := newGoalRig(t)
	ticks := make(chan time.Time, 1)
	armed := make(chan time.Duration, 2)
	timer := func(d time.Duration) (<-chan time.Time, func()) { armed <- d; return ticks, func() {} }
	r.s.batchTimer = timer
	ctx, cancel := context.WithCancel(r.ctx)
	caller := asSession(r.lead)
	ch, p := r.s.BoxChannel(ctx, caller, "")
	if p != nil {
		cancel()
		t.Fatal(p)
	}
	event, err := r.s.event(r.room, contract.Principal{Kind: contract.PrincipalEngine}, contract.PostCreated{Text: "follow-up"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.st.Post(r.ctx, event, []contract.Delivery{{To: caller, Surface: contract.SurfaceEngine, Reply: contract.ReplyChannel{Kind: contract.ReplyNone}, Wake: true}}); err != nil {
		t.Fatal(err)
	}
	select {
	case d := <-armed:
		if d != time.Minute {
			t.Fatalf("first delay %v", d)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("first timer not armed")
	}
	r.advance(20 * time.Second)
	cancel()
	for msg := range ch {
		t.Fatalf("early frame before restart: %T", msg.Body)
	}
	// Real store shutdown/reopen and Service reconstruction: no previous live
	// handle or timer survives, and the original turn end is still authoritative.
	r.restart()
	r.s.batchTimer = timer
	ch, p = testChannel(t, r.s, r.ctx, caller)
	if p != nil {
		t.Fatal(p)
	}
	select {
	case d := <-armed:
		if d != 40*time.Second {
			t.Fatalf("restart moved deadline: %v", d)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("restart did not schedule remaining window")
	}
	r.advance(40 * time.Second)
	ticks <- r.now()
	select {
	case msg := <-ch:
		start, ok := msg.Body.(contract.TurnStart)
		if !ok || len(start.Inputs) != 1 {
			t.Fatalf("restart frame %+v", msg.Body)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no turn at original deadline")
	}
}
