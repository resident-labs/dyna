package subagent

import (
	"context"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/hub"
)

func TestRunnerInputBurstChecksCanonicalStateOnce(t *testing.T) {
	e, info, input, _, writer := newInputWaitFixture(t)
	synctest.Test(t, func(t *testing.T) {
		// The subscription and cancellation belong to the virtual clock.
		// Database fixture setup stays outside the bubble.
		e.m.InputEvents = hub.New(1)
		parent, cancel := context.WithCancel(context.Background())
		defer cancel()
		ctx := authority.WithActor(parent, authority.Actor{Role: authority.Session, Session: info.ID})
		start := time.Now()
		checks := 0
		idle, held := make(chan struct{}), make(chan struct{})
		release := sync.OnceFunc(func() { close(held) })
		defer release()
		e.m.checkpoint = func(stage string) {
			if stage == "input-wait-check" {
				checks++
			}
			if stage == "input-wait-idle" && checks == 1 {
				close(idle)
				<-held
			}
		}
		type result struct {
			ready bool
			err   error
		}
		done := make(chan result, 1)
		go func() {
			ready, err := e.m.RunnerWaitInput(ctx, input)
			done <- result{ready, err}
		}()
		synctest.Wait()
		if checks != 1 {
			t.Fatalf("initial canonical checks = %d, want 1", checks)
		}
		select {
		case <-idle:
		default:
			t.Fatal("reader did not reach its initial idle checkpoint")
		}
		before := writer.Health().Writes
		// All events fit in the existing subscription buffer. They describe
		// the same unchanged state, so one authoritative recheck is enough.
		for range hub.Buffer / 2 {
			e.m.InputEvents.Publish(hub.Event{Topic: "members", Room: info.RoomID})
		}
		release()
		synctest.Wait()
		if checks != 2 {
			t.Fatalf("one queued event burst made %d canonical checks, want 2 including startup", checks)
		}
		if writes := writer.Health().Writes - before; writes != 0 {
			t.Fatalf("readiness burst wrote %d times", writes)
		}
		select {
		case got := <-done:
			t.Fatalf("unchanged state returned ready %v, error %v", got.ready, got.err)
		default:
		}
		msg := postClaimInput(t, e, info, "real input after the burst")
		afterPost := writer.Health().Writes
		e.m.InputEvents.Publish(hub.Event{Topic: "posts", Room: info.RoomID})
		synctest.Wait()
		select {
		case got := <-done:
			if got.err != nil || !got.ready || checks != 3 {
				t.Fatalf("real input: ready %v, error %v, checks %d", got.ready, got.err, checks)
			}
		default:
			t.Fatal("real input did not finish the readiness wait")
		}
		if writes := writer.Health().Writes - afterPost; writes != 0 {
			t.Fatalf("readiness claimed input with %d writes", writes)
		}
		claim, err := e.m.RunnerClaimInput(ctx, input)
		if err != nil || len(claim.Posts) != 1 || claim.Posts[0].Steer.ID != msg.ID {
			t.Fatalf("authoritative claim after readiness: %+v, %v", claim, err)
		}
		if elapsed := time.Since(start); elapsed != 0 {
			t.Fatalf("event-driven readiness advanced virtual time by %s", elapsed)
		}
	})
}
