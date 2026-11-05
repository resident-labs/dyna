package podman

import (
	"context"
	"fmt"
	"log"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

type ProbeOutcome uint8

const (
	ProbeUnknown ProbeOutcome = iota
	ProbeGood
	ProbeFailed
)

type Load struct {
	Probe                                  ProbeOutcome
	Health                                 contract.HostHealth
	CPUUsed                                float64
	MemBytes, MemUsed, DiskBytes, DiskUsed int64
	Services                               []box.ServiceUnit // The state of each service the host was asked about.
	ShareUp                                bool              // The host's shared mount is up.
	CPUs                                   int               // Cores of an exec-only host's whole machine; zero on other hosts and when it printed none.
	ExecOnly                               bool              // An exec-only host: the figures are the whole machine's, for display only.
	GPUs                                   *[]contract.GPU   // The cards the host has, when this beat probed for them: none is a probe that found none.
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
// A failed probe counts toward scheduling degradation even when the master is alive. Reachability is separate:
// only a dead master counts toward unreachable, so busy probes cannot disrupt running boxes.
// Each good probe also reads the dyna budget slices and calls onPressure once per episode. The caller owns the ticks.
func (h *Host) Heartbeat(ctx context.Context, ticks <-chan time.Time, layout box.Layout, report func(reachable bool, load Load), onPressure func(slice string, kind contract.PressureKind)) {
	misses, busy, beats := 0, false, 0
	failures := 0
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
			probe := "-"
			if beats%gpuProbeEvery == 0 {
				probe = "gpu"
			}
			var out []byte
			var err error
			if execOnly {
				out, err = h.runUnbounded(probeCtx, nil, "bash", "-c", execOnlyScript)
			} else {
				var ids []contract.ServiceID
				if h.services != nil {
					ids = h.services()
				}
				args := []string{"bash", "-c", heartbeatScript, "dyna", layout.HostData, layout.HostShareMount(), probe}
				for _, id := range ids {
					args = append(args, string(id))
				}
				out, err = h.runUnbounded(probeCtx, nil, args...)
			}
			cancel()
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				failures++
				load.Probe, load.GPUs = ProbeFailed, nil
				if failures >= box.ProbeFailureLimit {
					load.Health = contract.HostDegraded
				}
				if h.answering(ctx) {
					if !busy {
						log.Printf("host %s: heartbeat probe failed, but its SSH master is alive: %v", h.destination, err)
					}
					busy, misses = true, 0
					report(true, load)
					continue
				}
				busy = false
				misses++
				if misses >= box.ProbeFailureLimit {
					report(false, load)
				}
				continue
			}
			misses, busy, failures = 0, false, 0
			load.Health, load.Probe = contract.HostHealthy, ProbeGood
			load.GPUs = nil
			if probe == "gpu" && !execOnly {
				gpus := parseGPUs(out)
				load.GPUs = &gpus
			}
			beats++
			if execOnly {
				load = Load{ExecOnly: true, Health: contract.HostHealthy, Probe: ProbeGood}
				load.DiskBytes, load.DiskUsed = parseDisk(out)
				for _, line := range strings.Split(string(out), "\n") {
					var cpuBusy, total int64
					if n, _ := fmt.Sscanf(line, "dyna-cpus %d", &load.CPUs); n == 1 {
						continue
					}
					if n, _ := fmt.Sscanf(line, "dyna-mem %d %d", &load.MemBytes, &load.MemUsed); n == 2 {
						continue
					}
					if n, _ := fmt.Sscanf(line, "dyna-cpu %d %d", &cpuBusy, &total); n == 2 {
						if prevTotal > 0 && total > prevTotal && cpuBusy >= prevBusy {
							load.CPUUsed = float64(cpuBusy-prevBusy) / float64(total-prevTotal) * float64(load.CPUs)
						}
						prevBusy, prevTotal = cpuBusy, total
					}
				}
				report(true, load)
				continue
			}
			load.ExecOnly = false
			load.DiskBytes, load.DiskUsed = parseDisk(out)
			load.ShareUp = false
			load.Services = nil
			for _, line := range strings.Split(string(out), "\n") {
				if line == shareUpLine {
					load.ShareUp = true
				}
				if fields := strings.Fields(line); len(fields) > 2 && fields[0] == "dyna-svc" {
					unit, err := parseServiceUnit(contract.ServiceID(fields[1]), fields[2:])
					if err != nil {
						log.Printf("host %s: %v", h.destination, err)
						continue
					}
					load.Services = append(load.Services, unit)
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

// gpuProbeEvery is how many good beats pass between GPU probes, the first beat included: about five minutes at 10 s a beat.
const gpuProbeEvery = 30

// gpuScript prints the cards CDI offers and what nvidia-smi says of them, when the beat's third argument is gpu. Either tool may be
// missing, and a host without a card prints nothing.
const gpuScript = `if [ "$3" = gpu ]; then
timeout 5 nvidia-ctk cdi list 2>/dev/null | sed -n 's|^nvidia.com/gpu=\([0-9][0-9]*\)$|dyna-gpu-cdi \1|p'
timeout 5 nvidia-smi --query-gpu=index,name,memory.total --format=csv,noheader,nounits 2>/dev/null | sed 's|^|dyna-gpu |'
fi
`

// parseGPUs reads gpuScript's output. A card counts when CDI lists it, so a container can have it; nvidia-smi gives its name and
// memory, and a card it does not report has memory 0, which no request fits.
func parseGPUs(out []byte) []contract.GPU {
	offered := map[int]bool{}
	reported := map[int]contract.GPU{}
	for _, line := range strings.Split(string(out), "\n") {
		if text, ok := strings.CutPrefix(line, "dyna-gpu-cdi "); ok {
			if index, err := strconv.Atoi(text); err == nil {
				offered[index] = true
			}
		} else if text, ok := strings.CutPrefix(line, "dyna-gpu "); ok {
			// The name may hold ", ": the index is the first field and the memory the last.
			fields := strings.Split(text, ", ")
			if len(fields) < 3 {
				continue
			}
			index, indexErr := strconv.Atoi(fields[0])
			mib, memErr := strconv.ParseInt(fields[len(fields)-1], 10, 64)
			if indexErr == nil && memErr == nil {
				reported[index] = contract.GPU{Index: index, Name: strings.Join(fields[1:len(fields)-1], ", "), MemBytes: mib << 20}
			}
		}
	}
	var gpus []contract.GPU
	for index := range offered {
		card, ok := reported[index]
		if !ok {
			card = contract.GPU{Index: index}
		}
		gpus = append(gpus, card)
	}
	slices.SortFunc(gpus, func(a, b contract.GPU) int { return a.Index - b.Index })
	return gpus
}
