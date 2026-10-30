package podman

import (
	"context"
	"time"
)

// Ticker lets the caller supply a clock for heartbeats.
type Ticker interface {
	C() <-chan time.Time
	Stop()
}
type clockTicker struct{ *time.Ticker }

func (t clockTicker) C() <-chan time.Time { return t.Ticker.C }

// Heartbeat probes the host every ten seconds. It reports a state change
// after three consecutive misses and again when the host responds.
func (h *Host) Heartbeat(ctx context.Context, ticker Ticker, report func(reachable bool)) {
	if ticker == nil {
		ticker = clockTicker{time.NewTicker(10 * time.Second)}
	}
	defer ticker.Stop()
	misses := 0
	reachable := true
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C():
			probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			_, err := h.run(probeCtx, "true")
			cancel()
			if err != nil {
				misses++
				if misses >= 3 && reachable {
					reachable = false
					report(false)
				}
			} else {
				misses = 0
				if !reachable {
					reachable = true
					report(true)
				}
			}
		}
	}
}
