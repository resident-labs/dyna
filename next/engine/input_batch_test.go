package engine

import (
	"github.com/resident-labs/dyna/contract"
	"sync/atomic"
	"testing"
	"time"
)

// Exercise the actual framed stream: held inputs must not leak TurnStart.
func TestInputBatchFramedDeadline(t *testing.T) {
	s, room, worker, ctx := setup(t)
	var clock atomic.Int64
	clock.Store(s.now().UnixMilli())
	s.now = func() time.Time { return time.UnixMilli(clock.Load()) }
	ticks := make(chan time.Time, 1)
	armed := make(chan time.Duration, 4)
	s.batchTimer = func(d time.Duration) (<-chan time.Time, func()) { armed <- d; return ticks, func() {} }
	caller := boxCaller(worker)
	ch, problem := testChannel(t, s, ctx, caller)
	if problem != nil {
		t.Fatal(problem)
	}
	first := (<-ch).Body.(contract.TurnStart)
	if _, p := s.TurnOutput(ctx, caller, contract.TurnOutputRequest{Turn: first.Turn, Outcome: contract.TurnCompleted}); p != nil {
		t.Fatal(p)
	}
	post := func(id string) {
		t.Helper()
		e, err := s.event(room, contract.Principal{Kind: contract.PrincipalEngine}, contract.PostCreated{Text: id})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.store.Post(ctx, e, []contract.Delivery{{To: caller, Surface: contract.SurfaceEngine, Reply: contract.ReplyChannel{Kind: contract.ReplyNone}, Wake: true}}); err != nil {
			t.Fatal(err)
		}
	}
	post("one")
	select {
	case d := <-armed:
		if d != time.Minute {
			t.Fatalf("deadline delay %v", d)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timer not armed")
	}
	select {
	case msg := <-ch:
		t.Fatalf("early frame: %T", msg.Body)
	case <-time.After(100 * time.Millisecond):
	}
	clock.Add(20_000)
	post("two")
	select {
	case msg := <-ch:
		t.Fatalf("arrival extended or leaked frame: %T", msg.Body)
	case <-time.After(100 * time.Millisecond):
	}
	select {
	case d := <-armed:
		t.Fatalf("arrival replaced timer: %v", d)
	default:
	}
	clock.Add(40_000)
	// Fire the scheduler, not another input or a manual dispatch.
	ticks <- s.now()
	select {
	case msg := <-ch:
		start, ok := msg.Body.(contract.TurnStart)
		if !ok || len(start.Inputs) != 2 {
			t.Fatalf("deadline frame: %+v", msg.Body)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no deadline turn")
	}
}
