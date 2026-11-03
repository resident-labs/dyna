package runtime

import (
	"context"
	"log"
	"time"
)

const (
	shutdownTimeout = 15 * time.Second
	cleanupTimeout  = 3 * time.Second
)

type shutdownStep struct {
	name string
	done <-chan struct{}
	run  func()
}

// engineShutdown is Run's shutdown stack, in acquisition order. Worker joins
// share 15 s; final cleanup gets a separate 3 s so a stuck worker cannot consume
// the time needed to close sockets, stop the proxy and close the store.
type engineShutdown struct {
	stop    context.CancelFunc
	workers []shutdownStep
	final   []shutdownStep
}

func (s *engineShutdown) join(name string, done <-chan struct{}) {
	s.workers = append(s.workers, shutdownStep{name: name, done: done})
}

func (s *engineShutdown) cleanup(name string, run func()) {
	s.final = append(s.final, shutdownStep{name: name, run: run})
}

func (s *engineShutdown) run() {
	// SIGTERM already cancels the workers; stop matters on early-error returns.
	s.stop()
	workers, cancelWorkers := context.WithTimeout(context.Background(), shutdownTimeout)
	s.finish(workers, s.workers)
	cancelWorkers()
	final, cancelFinal := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancelFinal()
	s.finish(final, s.final)
}

func (*engineShutdown) finish(ctx context.Context, steps []shutdownStep) {
	for i := len(steps) - 1; i >= 0; i-- {
		step := steps[i]
		done := step.done
		if step.run != nil {
			finished := make(chan struct{})
			done = finished
			go func() {
				defer close(finished)
				step.run()
			}()
		}
		// Do not label an already finished worker late when its phase expired.
		select {
		case <-done:
			continue
		default:
		}
		select {
		case <-done:
		case <-ctx.Done():
			log.Printf("shutdown: %s exceeded its phase deadline", step.name)
		}
	}
}
