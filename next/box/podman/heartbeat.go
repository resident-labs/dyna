package podman

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/resident-labs/dyna/contract"
)

// Heartbeat reports load on each good tick and reports unreachable after three misses.
// Each good probe also reads the dyna budget slices and calls onPressure once per episode. The caller owns the ticks.
func (h *Host) Heartbeat(ctx context.Context, ticks <-chan time.Time, report func(reachable bool, cpu float64, mem int64), onPressure func(slice string, kind contract.PressureKind)) {
	misses := 0
	reachable := true
	seen := make(map[string]*slicePressure)
	var prevTime time.Time
	var prevCPU, mem int64
	var cpu float64
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
					report(false, cpu, mem)
				}
				continue
			}
			misses = 0
			reachable = true
			for _, line := range strings.Split(string(out), "\n") {
				if strings.HasPrefix(line, "dyna-load ") {
					var used, current int64
					if n, _ := fmt.Sscanf(line, "dyna-load %d %d", &current, &used); n == 2 {
						cpu = 0
						if !prevTime.IsZero() && now.After(prevTime) && used >= prevCPU {
							cpu = float64(used-prevCPU) / (now.Sub(prevTime).Seconds() * 1e6)
						}
						prevTime, prevCPU, mem = now, used, current
					}
				}
			}
			report(true, cpu, mem)
			events, err := parseSliceEvents(out)
			if err != nil {
				log.Printf("box pressure: skip slice output: %v", err)
				continue
			}
			observeSlices(seen, events, now, onPressure)
		}
	}
}
