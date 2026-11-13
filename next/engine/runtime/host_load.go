package runtime

import (
	"context"
	"fmt"
	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/box/podman"
	"github.com/resident-labs/dyna/contract"
	"log"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"time"
)

type heartbeatStore interface {
	RecordHostHeartbeat(context.Context, contract.HostName, bool, *int64, int64, time.Time) error
	SetHostGPUs(context.Context, contract.HostName, []contract.GPU) error
}

type hostLoad struct {
	summary contract.HostSummary
	beats   int
	cpuSum  float64
	memSum  int64
}

// cpuTimes holds the cumulative jiffies of the aggregate cpu line in /proc/stat.
type cpuTimes struct {
	busy, total uint64
}

func (b *Boxes) reportHeartbeat(ctx context.Context, name contract.HostName, ok bool, load podman.Load, now time.Time) {
	if b.HeartbeatStore == nil {
		return
	}
	var free *int64
	// An exec-only host's disk is shown, never stored: warnings, eviction and placement read the store's figures.
	if ok && load.Probe != podman.ProbeFailed && load.DiskBytes > 0 && !load.ExecOnly {
		value := load.DiskBytes - load.DiskUsed
		free = &value
	}
	total := load.DiskBytes
	if load.ExecOnly {
		total = 0
	}
	if err := b.HeartbeatStore.RecordHostHeartbeat(ctx, name, ok, free, total, now); err != nil {
		log.Printf("host %s: record heartbeat: %v", name, err)
		return
	}
	if ok && load.Probe != podman.ProbeFailed && load.GPUs != nil {
		if err := b.HeartbeatStore.SetHostGPUs(ctx, name, *load.GPUs); err != nil {
			log.Printf("host %s: record GPUs: %v", name, err)
		}
	}
	b.Pressure.Reconciler().Kick()
}

func (b *Boxes) HostLoad(ctx context.Context, hosts []contract.HostSummary) error {
	records, err := b.Store.Boxes(ctx)
	if err != nil {
		return err
	}
	counts := make(map[contract.HostName]int)
	for _, record := range records {
		if record.State == contract.BoxStarting || record.State == contract.BoxReady {
			counts[record.Host]++
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	starts := make(map[contract.SessionID]contract.BoxStartStatus)
	for _, record := range records {
		if record.State == contract.BoxCreating || record.State == contract.BoxStarting {
			if start, ok := b.starts[record.ID]; ok {
				starts[record.Session] = start
			}
		}
	}
	for i := range hosts {
		h := &hosts[i]
		load := b.loads[h.Name].summary
		h.Health = load.Health
		for j := range h.Sessions {
			if start, ok := starts[h.Sessions[j].ID]; ok {
				copy := start
				h.Sessions[j].Start = &copy
			}
		}
		h.Reachable, h.CPUUsed, h.MemUsed, h.Boxes = load.Reachable, load.CPUUsed, load.MemUsed, counts[h.Name]
		h.DiskBytes, h.DiskUsed, h.LastSeen = load.DiskBytes, load.DiskUsed, load.LastSeen
		h.History = append([]contract.HostLoadPoint{}, load.History...)
		cutoff := time.Now().Truncate(time.Minute).Add(-59 * time.Minute)
		for len(h.History) > 0 && h.History[0].At.Before(cutoff) {
			h.History = h.History[1:]
		}
		// A live sample uses the same floor rule as the stored heartbeat and its notice.
		if load.DiskBytes > 0 && !h.ExecOnly {
			h.DiskLow = box.DiskLow(load.DiskBytes-load.DiskUsed, box.DiskFloor(load.DiskBytes))
		}
		// An exec-only host has no setup check, so its capacity is what its probe measured.
		if h.ExecOnly {
			h.CPUs, h.MemBytes = load.CPUs, load.MemBytes
		}
		if h.Name == contract.ControlPlane {
			h.Reachable, h.CPUs, h.MemBytes = true, goruntime.NumCPU(), load.MemBytes
		}
	}
	return nil
}

// record keeps the latest figures and minute means under the shared lock.
func (b *Boxes) record(name contract.HostName, ok bool, sample podman.Load, now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.loads == nil {
		b.loads = make(map[contract.HostName]hostLoad)
	}
	state := b.loads[name]
	load := &state.summary
	load.Reachable = ok
	if sample.Probe != podman.ProbeUnknown {
		load.Health = sample.Health
	}
	if ok && sample.Probe != podman.ProbeFailed {
		load.LastSeen = now
		load.CPUUsed, load.MemUsed, load.DiskBytes, load.DiskUsed = sample.CPUUsed, sample.MemUsed, sample.DiskBytes, sample.DiskUsed
		load.MemBytes, load.CPUs = sample.MemBytes, sample.CPUs
		minute := now.Truncate(time.Minute)
		for len(load.History) > 0 && load.History[0].At.Before(minute.Add(-59*time.Minute)) {
			load.History = load.History[1:]
		}
		if len(load.History) == 0 || !load.History[len(load.History)-1].At.Equal(minute) {
			load.History = append(load.History, contract.HostLoadPoint{At: minute})
			state.beats, state.cpuSum, state.memSum = 0, 0, 0
		}
		state.beats++
		state.cpuSum += sample.CPUUsed
		state.memSum += sample.MemUsed
		point := &load.History[len(load.History)-1]
		point.CPUUsed, point.MemUsed = state.cpuSum/float64(state.beats), state.memSum/int64(state.beats)
	}
	b.loads[name] = state
}

func (b *Boxes) hostMemory(name contract.HostName) int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.loads[name].summary.MemUsed
}

func (b *Boxes) hostCPU(name contract.HostName) *float64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	load := b.loads[name].summary
	if load.LastSeen.IsZero() {
		return nil
	}
	return &load.CPUUsed
}

// controlPlane samples the engine host on the same schedule as remote hosts.
func (b *Boxes) controlPlane(data string) {
	root := b.Proc
	if root == "" {
		root = "/proc"
	}
	tick := time.NewTicker(10 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-b.Context.Done():
			return
		case now := <-tick.C:
			var load podman.Load
			if stat, err := os.ReadFile(filepath.Join(root, "stat")); err == nil {
				load.CPUUsed = b.cpuCores(string(stat), goruntime.NumCPU())
			}
			if mem, err := os.ReadFile(filepath.Join(root, "meminfo")); err == nil {
				load.MemBytes, load.MemUsed = memory(string(mem))
			}
			load.DiskBytes, load.DiskUsed = podman.LocalDisk(b.Context, data)
			b.record(contract.ControlPlane, true, load, now)
			b.reportHeartbeat(b.Context, contract.ControlPlane, true, load, now)
		}
	}
}

// cpuCores returns the cores busy since the previous sample and keeps this sample. The first sample reports zero.
func (b *Boxes) cpuCores(stat string, cpus int) float64 {
	line, _, _ := strings.Cut(stat, "\n")
	var user, nice, system, idle, iowait, irq, softirq, steal uint64
	if n, _ := fmt.Sscanf(line, "cpu %d %d %d %d %d %d %d %d", &user, &nice, &system, &idle, &iowait, &irq, &softirq, &steal); n != 8 {
		return 0
	}
	total := user + nice + system + idle + iowait + irq + softirq + steal
	now := cpuTimes{busy: total - idle - iowait, total: total}
	prev := b.cpuPrev
	b.cpuPrev = &now
	if prev == nil || now.total <= prev.total || now.busy < prev.busy {
		return 0
	}
	return float64(now.busy-prev.busy) / float64(now.total-prev.total) * float64(cpus)
}

// memory returns the total and used bytes from the text of /proc/meminfo.
func memory(meminfo string) (total, used int64) {
	var avail int64
	for _, line := range strings.Split(meminfo, "\n") {
		fmt.Sscanf(line, "MemTotal: %d", &total)
		fmt.Sscanf(line, "MemAvailable: %d", &avail)
	}
	return total * 1024, (total - avail) * 1024
}
