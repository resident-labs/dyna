package podman

import (
	"context"
	"log"
	"time"

	"github.com/resident-labs/dyna/contract"
)

// Heartbeat probes the host on each tick. It reports a state change after three misses and when the host responds.
// Each good probe also reads the dyna budget slices and calls onPressure once per episode. The caller owns the ticks.
func (h *Host) Heartbeat(ctx context.Context, ticks <-chan time.Time, report func(reachable bool), onPressure func(slice string, kind contract.PressureKind)) {
	misses := 0
	reachable := true
	seen := make(map[string]*slicePressure)
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticks:
			probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			out, err := h.run(probeCtx, "bash", "-c", heartbeatScript)
			cancel()
			if err != nil {
				misses++
				if misses >= 3 && reachable {
					reachable = false
					report(false)
				}
				continue
			}
			misses = 0
			if !reachable {
				reachable = true
				report(true)
			}
			events, err := parseSliceEvents(out)
			if err != nil {
				log.Printf("box pressure: skip slice output: %v", err)
				continue
			}
			observeSlices(seen, events, now, onPressure)
		}
	}
}
