package engine

import (
	"context"
	"net/http"
	"sync"
	"time"
)

// shutdownTimeout bounds engine stop. A local CI run needs a few seconds to stop
// its containers and then its keeper on the machine, and a session worker can
// wait three seconds for a handoff, so the bound covers them. Nothing waits for
// the full time when the work ends sooner.
const shutdownTimeout = 5 * time.Second

// stopEngine cancels every engine service before joining cleanup. HTTP, workers
// and foreground process cleanup share one deadline, including error exits.
func stopEngine(cancel context.CancelFunc, workers <-chan struct{}, servers []*http.Server, cleanup ...func()) error {
	cancel()
	ctx, stop := context.WithTimeout(context.Background(), shutdownTimeout)
	defer stop()
	defer func() {
		for _, server := range servers {
			_ = server.Close()
		}
	}()
	var pending sync.WaitGroup
	start := func(fn func()) {
		pending.Add(1)
		go func() { defer pending.Done(); fn() }()
	}
	for _, server := range servers {
		start(func() { _ = server.Shutdown(ctx) })
	}
	if workers != nil {
		start(func() { <-workers })
	}
	for _, closeService := range cleanup {
		start(closeService)
	}
	done := make(chan struct{})
	go func() { pending.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
