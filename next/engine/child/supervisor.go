// Package child supervises engine child processes.
package child

import (
	"context"
	"time"
)

// Run restarts a child with bounded backoff. A wake cancels and replaces it.
// A nil result channel waits for a wake without starting a child.
func Run(ctx context.Context, wake <-chan struct{}, now func() time.Time, after func(time.Duration) <-chan time.Time, start func(context.Context) <-chan error, exited func(error)) {
	delay := time.Second
	for ctx.Err() == nil {
		child, cancel := context.WithCancel(ctx)
		began := now()
		done := start(child)
		select {
		case <-ctx.Done():
			cancel()
			if done != nil {
				<-done
			}
			return
		case <-wake:
			cancel()
			if done != nil {
				<-done
			}
			delay = time.Second
			continue
		case err := <-done:
			cancel()
			if ctx.Err() != nil {
				return
			}
			exited(err)
		}
		if now().Sub(began) >= time.Minute {
			delay = time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-wake:
			delay = time.Second
			continue
		case <-after(delay):
		}
		if delay < 30*time.Second {
			delay = min(delay*2, 30*time.Second)
		}
	}
}
