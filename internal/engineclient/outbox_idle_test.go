package engineclient

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
)

func TestWaitIdleCancellationKeepsMutationOrder(t *testing.T) {
	release, arrived := make(chan struct{}), make(chan struct{})
	engine := &fakeEngine{held: release, arrived: arrived}
	out := NewOutbox(serveFake(t, engine), "session/turn")
	defer out.Close(t.Context())
	var once sync.Once
	releaseWrite := func() { once.Do(func() { close(release) }) }
	defer releaseWrite()
	out.Send("First")
	awaitTerminal(t, arrived)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- out.WaitIdle(ctx) }()
	cancel()
	if err := awaitTerminal(t, done); !errors.Is(err, context.Canceled) {
		releaseWrite()
		t.Fatalf("cancelled observer = %v", err)
	}
	out.mu.Lock()
	expired, closed, queued := out.spoolOnly, out.closed, len(out.queue)
	out.mu.Unlock()
	out.Send("Second")
	releaseWrite()
	if expired || closed || queued != 1 {
		t.Fatalf("observer changed outbox: expired %v, closed %v, queued %d", expired, closed, queued)
	}
	if err := out.WaitIdle(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := engine.seen(); !slices.Equal(got, []string{"First session/turn/1", "Second session/turn/2"}) {
		t.Fatalf("ordered calls = %v", got)
	}
}
