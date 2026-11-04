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
	CPUs                                   int  // Cores of an exec-only host's whole machine; zero on other hosts and when it printed none.
	ExecOnly                               bool // An exec-only host: the figures are the whole machine's, for display only.
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

// execOnlyScript prints the whole machine's figures for an exec-only host, which has no dyna.slice and no Dyna data folder. The
// disk is the home folder's filesystem by plain df: it is only shown, so the btrfs adjustment of diskScript would mislead.
// A line is missing where the host cannot say it, and the script always succeeds, so such a host is still just online.
const execOnlyScript = `read -r size avail < <(df -B1 --output=size,avail "${HOME:-/}" 2>/dev/null | awk 'NR == 2 {print $1, $2}'); [ -z "$size" ] || echo "dyna-disk $size $((size - avail))"
echo "dyna-cpus $(nproc 2>/dev/null)"
awk '/^cpu / {t = 0; for (i = 2; i <= 9; i++) t += $i; printf "dyna-cpu %.0f %.0f\n", t - $5 - $6, t}' /proc/stat 2>/dev/null
awk '$1 == "MemTotal:" {t = $2} $1 == "MemAvailable:" {a = $2} END {if (t) printf "dyna-mem %.0f %.0f\n", t * 1024, (t - a) * 1024}' /proc/meminfo 2>/dev/null
exit 0`

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
	var prevCPU, prevBusy, prevTotal int64
	var load Load
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticks:
			probeCtx, cancel := context.WithTimeout(ctx, heartbeatProbe)
			execOnly := h.execOnly != nil && h.execOnly()
			var out []byte
			var err error
			if execOnly {
				out, err = h.runUnbounded(probeCtx, nil, "bash", "-c", execOnlyScript)
			} else {
				out, err = h.runUnbounded(probeCtx, nil, "bash", "-c", heartbeatScript, "dyna", layout.HostData, layout.HostShareMount())
			}
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
			if execOnly {
				load = Load{ExecOnly: true}
				load.DiskBytes, load.DiskUsed = parseDisk(out)
				for _, line := range strings.Split(string(out), "\n") {
					var busy, total int64
					if n, _ := fmt.Sscanf(line, "dyna-cpus %d", &load.CPUs); n == 1 {
						continue
					}
					if n, _ := fmt.Sscanf(line, "dyna-mem %d %d", &load.MemBytes, &load.MemUsed); n == 2 {
						continue
					}
					if n, _ := fmt.Sscanf(line, "dyna-cpu %d %d", &busy, &total); n == 2 {
						if prevTotal > 0 && total > prevTotal && busy >= prevBusy {
							load.CPUUsed = float64(busy-prevBusy) / float64(total-prevTotal) * float64(load.CPUs)
						}
						prevBusy, prevTotal = busy, total
					}
				}
				report(true, load)
				continue
			}
			load.ExecOnly = false
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
