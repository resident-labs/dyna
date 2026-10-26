package engine

import (
	"context"
	"net/http"
	"sync"
	"time"

	"dyna-agent/internal/localci"
)

// shutdownTimeout bounds engine stop. The longest work it waits for is a local
// CI run that the stop cancelled and that now removes itself from its machine:
// it stops the containers and then the keeper, which localCIStop bounds. The
// extra second covers reaching that wait. Nothing waits for the full time when
// the work ends sooner.
const shutdownTimeout = localCIStop + time.Second

// streamShutdownTimeout bounds the HTTP phase on its own. A page or a terminal
// keeps a stream open, and a stream never ends by itself, so waiting for one to
// finish made a restart sit for the whole shutdown budget while every dyna
// command failed. After this bound the connections are closed instead.
const streamShutdownTimeout = 3 * time.Second

// streamShutdownDeadline is the bound a stop uses. A test shortens it, so a
// regression test does not wait the production three seconds.
var streamShutdownDeadline = func() time.Duration { return streamShutdownTimeout }

// stopEngine cancels every engine service before joining cleanup. HTTP, workers
// and foreground process cleanup share one deadline, including error exits.
func stopEngine(cancel context.CancelFunc, workers <-chan struct{}, servers []*http.Server, cleanup ...func()) error {
	// Tell the local CI runner that a cancellation from here is Dyna's own stop,
	// not evidence about the commit: that run is repeated, not counted.
	localci.MarkEngineStopping()
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
		start(func() {
			// A graceful stop ends a request that finishes, and then this deadline
			// closes what is left, such as an open event stream.
			streamCtx, cancelStream := context.WithTimeout(context.Background(), streamShutdownDeadline())
			defer cancelStream()
			_ = server.Shutdown(streamCtx)
			_ = server.Close()
		})
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
