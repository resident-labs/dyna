package podman

import (
	"context"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

type Load struct {
	CPUUsed                                float64
	MemBytes, MemUsed, DiskBytes, DiskUsed int64
	ShareUp                                bool // The host's shared mount is up.
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

// heartbeatProbe bounds one probe. It includes the wait for the host's gate, and a host under heavy load can take many
// seconds to start a shell, so it is generous.
const heartbeatProbe = 20 * time.Second

// masterCheck bounds the check that the SSH master is alive. The check only asks the local master process.
const masterCheck = 5 * time.Second

// Heartbeat reports load on each good tick and reports unreachable after three misses in a row.
// A probe that fails while the SSH master is alive is not a miss: the master's keepalives still get answers, so the host is up,
// only too busy to run the probe in time. A host that is really gone stops answering them, and the master ends.
// Each good probe also reads the dyna budget slices and calls onPressure once per episode. The caller owns the ticks.
func (h *Host) Heartbeat(ctx context.Context, ticks <-chan time.Time, layout box.Layout, report func(reachable bool, load Load), onPressure func(slice string, kind contract.PressureKind)) {
	misses, busy := 0, false
	seen := make(map[string]*slicePressure)
	var prevTime time.Time
	var prevCPU int64
	var load Load
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticks:
			probeCtx, cancel := context.WithTimeout(ctx, heartbeatProbe)
			out, err := h.runUnbounded(probeCtx, nil, "bash", "-c", heartbeatScript, "dyna", layout.HostData, layout.HostShareMount())
			cancel()
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				if h.answering(ctx) {
					if !busy {
						log.Printf("host %s: heartbeat probe failed, but its SSH master is alive: %v", h.destination, err)
					}
					busy, misses = true, 0
					continue
				}
				busy = false
				misses++
				if misses >= 3 {
					report(false, load)
				}
				continue
			}
			misses, busy = 0, false
			load.DiskBytes, load.DiskUsed = parseDisk(out)
			load.ShareUp = false
			for _, line := range strings.Split(string(out), "\n") {
				if line == shareUpLine {
					load.ShareUp = true
				}
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

// answering reports whether the SSH master is alive. It sends a keepalive every 5 s and ends after 15 s without an answer,
// so a live master means the host answered within that time.
func (h *Host) answering(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, masterCheck)
	defer cancel()
	return h.control(ctx, "check") == nil
}
