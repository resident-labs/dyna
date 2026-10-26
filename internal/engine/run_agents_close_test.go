package engine

import (
	"context"
	"testing"

	"dyna-agent/internal/hub"
)

func TestRunStateEventsCloseTheAgentsOfEndedRuns(t *testing.T) {
	events := hub.New(1)
	ch, _, stop := events.Subscribe()
	defer stop()
	ctx, cancel := context.WithCancel(t.Context())
	closed := make(chan struct{}, 2)
	done := make(chan struct{})
	go func() {
		defer close(done)
		closeAgentsOfEndedRuns(ctx, ch, func(context.Context) error { closed <- struct{}{}; return nil }, func(string, error) {})
	}()
	events.Publish(hub.Event{Topic: "rooms", Key: "other"})
	events.Publish(hub.Event{Topic: "runs", Key: "wf_1"})
	<-closed
	cancel()
	<-done
	if len(closed) != 0 {
		t.Fatalf("an event of another topic also closed run agents")
	}
}
