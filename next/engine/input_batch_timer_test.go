package engine

import (
	"github.com/resident-labs/dyna/contract"
	"sync/atomic"
	"testing"
	"time"
)

type inputBatchTestTimer struct {
	ticks   chan time.Time
	delay   time.Duration
	stopped atomic.Bool
}

func TestInputBatchTimerLifecycle(t *testing.T) {
	for _, mode := range []string{"late", "replacement", "owner", "early"} {
		t.Run(mode, func(t *testing.T) {
			s, room, worker, ctx := setup(t)
			var clock atomic.Int64
			clock.Store(s.now().UnixMilli())
			s.now = func() time.Time { return time.UnixMilli(clock.Load()) }
			armed := make(chan *inputBatchTestTimer, 8)
			s.batchTimer = func(d time.Duration) (<-chan time.Time, func()) {
				timer := &inputBatchTestTimer{ticks: make(chan time.Time, 1), delay: d}
				armed <- timer
				return timer.ticks, func() { timer.stopped.Store(true) }
			}
			nextTimer := func() *inputBatchTestTimer {
				t.Helper()
				select {
				case tm := <-armed:
					return tm
				case <-time.After(2 * time.Second):
					t.Fatal("no timer")
					return nil
				}
			}
			receive := func(ch <-chan contract.BoxMessage, n int) contract.TurnStart {
				t.Helper()
				select {
				case m := <-ch:
					start, ok := m.Body.(contract.TurnStart)
					if !ok || len(start.Inputs) != n {
						t.Fatalf("got %#v, want %d inputs", m.Body, n)
					}
					return start
				case <-time.After(2 * time.Second):
					t.Fatal("no TurnStart")
					return contract.TurnStart{}
				}
			}
			quiet := func(ch <-chan contract.BoxMessage) {
				t.Helper()
				select {
				case m := <-ch:
					t.Fatalf("unexpected message %#v", m)
				case <-time.After(80 * time.Millisecond):
				}
			}
			caller := boxCaller(worker)
			ch, p := testChannel(t, s, ctx, caller)
			if p != nil {
				t.Fatal(p)
			}
			first := receive(ch, 1)
			if _, p = s.TurnOutput(ctx, caller, contract.TurnOutputRequest{Turn: first.Turn, Outcome: contract.TurnCompleted}); p != nil {
				t.Fatal(p)
			}
			post := func(text string) {
				t.Helper()
				e, err := s.event(room, contract.Principal{Kind: contract.PrincipalEngine}, contract.PostCreated{Text: text})
				if err != nil {
					t.Fatal(err)
				}
				_, err = s.store.Post(ctx, e, []contract.Delivery{{To: caller, Surface: contract.SurfaceEngine, Reply: contract.ReplyChannel{Kind: contract.ReplyNone}, Wake: true}})
				if err != nil {
					t.Fatal(err)
				}
			}
			post("first held")
			tm := nextTimer()
			if tm.delay != time.Minute {
				t.Fatal(tm.delay)
			}
			clock.Add(30_000)
			post("second held")
			quiet(ch)
			select {
			case <-armed:
				t.Fatal("deadline replaced on arrival")
			default:
			}
			switch mode {
			case "replacement":
				old := ch
				ch, p = testChannel(t, s, ctx, caller)
				if p != nil {
					t.Fatal(p)
				}
				tm2 := nextTimer()
				if tm2.delay != 30*time.Second {
					t.Fatal(tm2.delay)
				}
				select {
				case _, ok := <-old:
					if ok {
						t.Fatal("old stream live")
					}
				case <-time.After(2 * time.Second):
					t.Fatal("old stream did not close")
				}
				if !tm.stopped.Load() {
					t.Fatal("old timer not canceled")
				}
				tm.ticks <- s.now()
				quiet(ch)
				clock.Add(30_000)
				tm2.ticks <- s.now()
				receive(ch, 2)
			case "owner":
				if _, p = s.Post(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.PostRequest{Room: room, Home: contract.Conversation{Kind: contract.ConversationDM, Session: worker}, Text: "owner now"}); p != nil {
					t.Fatal(p)
				}
				receive(ch, 3)
				// Cancellation occurs after the unbuffered TurnStart handoff.
				for i := 0; i < 100 && !tm.stopped.Load(); i++ {
					time.Sleep(time.Millisecond)
				}
				if !tm.stopped.Load() {
					t.Fatal("owner did not cancel timer")
				}
				clock.Add(90_000)
				tm.ticks <- s.now()
				quiet(ch)
			case "late":
				clock.Add(45_000)
				tm.ticks <- s.now()
				receive(ch, 2)
				quiet(ch)
			case "early":
				// A monotonic timer can fire before a wall-clock deadline if wall time
				// steps backwards after arming. It must not consume the only kick.
				clock.Add(29_000)
				tm.ticks <- s.now()
				quiet(ch)
				select {
				case replacement := <-armed:
					if replacement.delay != time.Second {
						t.Fatalf("rearm delay %v", replacement.delay)
					}
					clock.Add(1_000)
					replacement.ticks <- s.now()
					receive(ch, 2)
				case <-time.After(300 * time.Millisecond):
					t.Fatal("early kick consumed; unchanged batchAt prevents rearm")
				}
			}
		})
	}
}
