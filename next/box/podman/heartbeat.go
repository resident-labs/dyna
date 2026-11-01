package podman

import (
	"context"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"time"

	"github.com/resident-labs/dyna/contract"
)

type Load struct {
	CPUUsed                                float64
	MemBytes, MemUsed, DiskBytes, DiskUsed int64
}

// LocalDisk reads the disk at path on this machine with the host probe. It returns zeros when the probe fails.
func LocalDisk(ctx context.Context, path string) (bytes, used int64) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "-c", diskScript, "dyna", path)
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	if err != nil {
		return 0, 0
	}
	return parseDisk(out)
}

// parseDisk reads the dyna-disk line of a probe. A probe without one reports zeros.
func parseDisk(out []byte) (bytes, used int64) {
	for _, line := range strings.Split(string(out), "\n") {
		if n, _ := fmt.Sscanf(line, "dyna-disk %d %d", &bytes, &used); n == 2 {
			return bytes, used
		}
	}
	return 0, 0
}

// Heartbeat reports load on each good tick and reports unreachable after three misses.
// Each good probe also reads the dyna budget slices and calls onPressure once per episode. The caller owns the ticks.
func (h *Host) Heartbeat(ctx context.Context, ticks <-chan time.Time, data string, report func(reachable bool, load Load), onPressure func(slice string, kind contract.PressureKind)) {
	misses := 0
	seen := make(map[string]*slicePressure)
	var prevTime time.Time
	var prevCPU int64
	var load Load
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticks:
			probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			out, err := h.runUnbounded(probeCtx, nil, "bash", "-c", heartbeatScript, "dyna", data)
			cancel()
			if err != nil {
				misses++
				if misses >= 3 {
					report(false, load)
				}
				continue
			}
			misses = 0
			load.DiskBytes, load.DiskUsed = parseDisk(out)
			for _, line := range strings.Split(string(out), "\n") {
				if strings.HasPrefix(line, "dyna-load ") {
					var used, current int64
					if n, _ := fmt.Sscanf(line, "dyna-load %d %d", &current, &used); n == 2 {
						load.CPUUsed = 0
						if !prevTime.IsZero() && now.After(prevTime) && used >= prevCPU {
							load.CPUUsed = float64(used-prevCPU) / (now.Sub(prevTime).Seconds() * 1e6)
						}
						prevTime, prevCPU, load.MemUsed = now, used, current
					}
				}
			}
			report(true, load)
			events, err := parseSliceEvents(out)
			if err != nil {
				log.Printf("box pressure: skip slice output: %v", err)
				continue
			}
			observeSlices(seen, events, now, onPressure)
		}
	}
}
