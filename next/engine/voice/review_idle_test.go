package voice

import (
	"context"
	"testing"
	"time"
)

func TestReviewIdleIgnoresHeartbeat(t *testing.T) {
	page, browser := pair(t)
	provider, fake := pair(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, page, provider, nil, nil, callLimits{time.Second, 25 * time.Millisecond, time.Hour})
	}()
	expect(t, fake, "session.update")
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	limit := time.NewTimer(100 * time.Millisecond)
	defer limit.Stop()
	for {
		select {
		case <-done:
			return
		case <-tick.C:
			send(t, browser, message{"type": "played", "audio_end_ms": 0})
		case <-limit.C:
			t.Fatal("idle call survives four idle periods on browser heartbeats alone")
		}
	}
}
