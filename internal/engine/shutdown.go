package engine

import (
	"context"
	"net/http"
	"sync"
	"time"
)

// shutdownTimeout bounds engine stop. The longest work it waits for is a local
// CI run that the stop cancelled and that now removes itself from its machine:
// it stops the containers and then the keeper, which localCIStop bounds. The
// extra second covers reaching that wait. Nothing waits for the full time when
// the work ends sooner.
const shutdownTimeout = localCIStop + time.Second

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
