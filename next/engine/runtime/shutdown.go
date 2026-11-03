package runtime

import (
	"context"
	"log"
	"time"
)

const shutdownTimeout = 15 * time.Second

// shutdownWait gives all post-drain joins and cleanup one budget, not one per
// subsystem. Start it lazily so the admitted model calls keep their full drain.
// A timed-out worker is left to process exit; it must not delay the other cleanup.
type shutdownWait struct {
	start  func() (context.Context, context.CancelFunc)
	ctx    context.Context
	cancel context.CancelFunc
}

func (s *shutdownWait) wait(name string, done <-chan struct{}) {
	if s.ctx == nil {
		s.ctx, s.cancel = s.start()
	}
	// Prefer an already joined worker when the shared deadline is also ready.
	select {
	case <-done:
		return
	default:
	}
	select {
	case <-done:
	case <-s.ctx.Done():
		log.Printf("shutdown: %s did not finish within the shared %s budget", name, shutdownTimeout)
	}
}

func (s *shutdownWait) run(name string, cleanup func()) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		cleanup()
	}()
	s.wait(name, done)
}

func (s *shutdownWait) close() {
	if s.cancel != nil {
		s.cancel()
	}
}
