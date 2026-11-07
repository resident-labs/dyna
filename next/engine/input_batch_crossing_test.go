package engine

import (
	"github.com/resident-labs/dyna/contract"
	"sync/atomic"
	"testing"
	"time"
)

func TestInputBatchDeadlineCrossedDuringDispatch(t *testing.T) {
	s, room, worker, ctx := setup(t)
	var clock, step atomic.Int64
	clock.Store(s.now().UnixMilli())
	s.now = func() time.Time { return time.UnixMilli(clock.Add(step.Load())) }
	caller := boxCaller(worker)
	ch, p := testChannel(t, s, ctx, caller)
	if p != nil {
		t.Fatal(p)
	}
	start := (<-ch).Body.(contract.TurnStart)
	ended := s.now()
	if _, p = s.TurnOutput(ctx, caller, contract.TurnOutputRequest{Turn: start.Turn, Outcome: contract.TurnCompleted}); p != nil {
		t.Fatal(p)
	}
	ev, err := s.event(room, contract.Principal{Kind: contract.PrincipalEngine}, contract.PostCreated{Text: "arriving at the boundary"})
	if err != nil {
		t.Fatal(err)
	}
	clock.Store(ended.Add(time.Minute).UnixMilli() - 3)
	// ID and event timestamps precede the deadline; the next clock read crosses
	// it. No timer is fired and no unrelated input is sent to rescue this wake.
	step.Store(1)
	if _, err = s.store.Post(ctx, ev, []contract.Delivery{{To: caller, Surface: contract.SurfaceEngine, Reply: contract.ReplyChannel{Kind: contract.ReplyNone}, Wake: true}}); err != nil {
		t.Fatal(err)
	}
	select {
	case msg := <-ch:
		if _, ok := msg.Body.(contract.TurnStart); !ok {
			t.Fatalf("got %T", msg.Body)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("deadline crossing lost the wake")
	}
}
