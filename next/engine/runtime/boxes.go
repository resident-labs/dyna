// Package runtime connects the engine to host transports.
package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/box/images"
	"github.com/resident-labs/dyna/box/podman"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine"
	"github.com/resident-labs/dyna/engine/api"
	enginegit "github.com/resident-labs/dyna/engine/git"
	"github.com/resident-labs/dyna/engine/hostexec"
	engineimages "github.com/resident-labs/dyna/engine/images"
	"github.com/resident-labs/dyna/engine/lifecycle"
	"github.com/resident-labs/dyna/engine/store"
)

type boxStore interface {
	Hosts(context.Context) ([]box.Host, error)
	AddHost(context.Context, box.Host) error
	SetEngineHost(context.Context, contract.HostName, bool) error
	Session(context.Context, contract.SessionID) (store.Session, error)
	Box(context.Context, contract.BoxID) (box.Box, error)
	Boxes(context.Context) ([]box.Box, error)
	OpenRooms(context.Context) ([]contract.RoomID, error)
}

type heartbeatStore interface {
	RecordHostHeartbeat(context.Context, contract.HostName, bool, *int64, int64, time.Time) error
}

type Boxes struct {
	HeartbeatStore    heartbeatStore
	Store             boxStore
	Sockets           *api.Sockets
	Runtime           string
	Layout            box.Layout
	instructionsMu    sync.Mutex
	hostRunner        podman.Runner
	machineIDFile     string
	instructionSaves  atomic.Uint64
	instructionSynced map[*podman.Host]*instructionHost
	Version           string
	PiTarball         []byte
	Mirror            *enginegit.Mirror
	Images            *engineimages.Builder
	Context           context.Context
	Pressure          *engine.Service
	Proc              string // Proc is the folder with stat and meminfo; empty means /proc.
	mu                sync.Mutex
	hosts             map[contract.HostName]*podman.Host
	restored          map[contract.HostName]bool
	loads             map[contract.HostName]hostLoad
	cpuPrev           *cpuTimes // cpuPrev is the last /proc/stat sample of this machine, nil before the first.
	binOnce           sync.Once
	binSum            string
	binErr            error
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

// installBinary installs the engine on a host. It hashes the executable once and reads its bytes only if the host differs.
func (b *Boxes) installBinary(ctx context.Context, h *podman.Host) error {
	b.binOnce.Do(func() { b.binSum, b.binErr = executableSum() })
	if b.binErr != nil {
		return b.binErr
	}
	return h.InstallBinary(ctx, b.Layout.HostData, b.Version, b.binSum, readExecutable)
}

func executableSum() (string, error) {
	binary, err := readExecutable()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(binary)
	return hex.EncodeToString(sum[:]), nil
}

func readExecutable() ([]byte, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	return os.ReadFile(executable)
}

// cache stores a host driver and starts its heartbeat until the engine stops. A cached name keeps its first driver and heartbeat. The caller holds b.mu.
func (b *Boxes) cache(name contract.HostName, h *podman.Host) {
	if b.hosts == nil {
		b.hosts = make(map[contract.HostName]*podman.Host)
	}
	if b.hosts[name] != nil {
		return
	}
	b.hosts[name] = h
	ticker := time.NewTicker(10 * time.Second)
	go func() {
		defer ticker.Stop()
		h.Heartbeat(b.Context, ticker.C, b.Layout.HostData, func(ok bool, load podman.Load) {
			now := time.Now()
			if ok {
				if err := b.syncInstructions(b.Context, h); err != nil {
					log.Printf("host %s: instructions: %v", name, err)
				}
			}
			b.record(name, ok, load, now)
			b.reportHeartbeat(b.Context, name, ok, load, now)
		}, b.pressure(name))
	}()
}

func (b *Boxes) reportHeartbeat(ctx context.Context, name contract.HostName, ok bool, load podman.Load, now time.Time) {
	if b.HeartbeatStore == nil {
		return
	}
	var free *int64
	if ok && load.DiskBytes > 0 {
		value := load.DiskBytes - load.DiskUsed
		free = &value
	}
	if err := b.HeartbeatStore.RecordHostHeartbeat(ctx, name, ok, free, load.DiskBytes, now); err != nil {
		log.Printf("host %s: record heartbeat: %v", name, err)
		return
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
	for i := range hosts {
		h := &hosts[i]
		load := b.loads[h.Name].summary
		h.Reachable, h.CPUUsed, h.MemUsed, h.Boxes = load.Reachable, load.CPUUsed, load.MemUsed, counts[h.Name]
		h.DiskBytes, h.DiskUsed, h.LastSeen = load.DiskBytes, load.DiskUsed, load.LastSeen
		h.History = append([]contract.HostLoadPoint{}, load.History...)
		cutoff := time.Now().Truncate(time.Minute).Add(-59 * time.Minute)
		for len(h.History) > 0 && h.History[0].At.Before(cutoff) {
			h.History = h.History[1:]
		}
		if h.Name == contract.ControlPlane {
			h.Reachable, h.CPUs, h.MemBytes = true, goruntime.NumCPU(), load.MemBytes
			h.DiskLow = load.DiskBytes > 0 && load.DiskBytes-load.DiskUsed < box.DiskWarn(box.DiskFloor(load.DiskBytes))
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
	if ok {
		load.CPUUsed, load.MemUsed, load.DiskBytes, load.DiskUsed, load.LastSeen = sample.CPUUsed, sample.MemUsed, sample.DiskBytes, sample.DiskUsed, now
		load.MemBytes = sample.MemBytes
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

func (b *Boxes) host(ctx context.Context, name contract.HostName) (*podman.Host, error) {
	b.mu.Lock()
	h := b.hosts[name]
	b.mu.Unlock()
	if h != nil {
		return h, nil
	}
	hosts, err := b.Store.Hosts(ctx)
	if err != nil {
		return nil, err
	}
	for _, record := range hosts {
		if record.Name == name {
			h, err := podman.New(record.SSH, b.Runtime, string(name), nil)
			if err != nil {
				return nil, err
			}
			same, err := b.engineHost(ctx, h)
			if err != nil {
				return nil, err
			}
			if err := b.Store.SetEngineHost(ctx, name, same); err != nil {
				return nil, err
			}
			b.mu.Lock()
			if existing := b.hosts[name]; existing != nil {
				b.mu.Unlock()
				return existing, nil
			}
			if b.loads == nil {
				b.loads = make(map[contract.HostName]hostLoad)
			}
			b.loads[name] = hostLoad{summary: contract.HostSummary{Reachable: record.State == box.HostReady}}
			b.cache(name, h)
			b.mu.Unlock()
			return h, nil
		}
	}
	return nil, fmt.Errorf("host %q: %w", name, store.ErrNotFound)
}

func (b *Boxes) MaintainHost(ctx context.Context, name contract.HostName) ([]contract.RoomShared, error) {
	h, err := b.host(ctx, name)
	if err != nil {
		return nil, err
	}
	return h.Maintain(ctx, b.Layout.HostData)
}

// RemoveRoom removes a room's folder from a host.
func (b *Boxes) RemoveRoom(ctx context.Context, name contract.HostName, room contract.RoomID) error {
	dir, err := b.Layout.HostRoom(room)
	if err != nil {
		return err
	}
	h, err := b.host(ctx, name)
	if err != nil {
		return err
	}
	b.instructionState(h).settle()
	return h.RemoveRoom(ctx, dir)
}

// RemoveLeftoverBox removes a box that an offline host still held when its room was deleted. The box has no row any more.
func (b *Boxes) RemoveLeftoverBox(ctx context.Context, name contract.HostName, id contract.BoxID, slice string) error {
	h, err := b.host(ctx, name)
	if err != nil {
		return err
	}
	return removeHostBox(ctx, h, box.Box{ID: id, Slice: slice}, b.forward(id))
}

// ForgetBox closes the engine's socket for a box that its offline host still holds.
func (b *Boxes) ForgetBox(id contract.BoxID) error { return b.Sockets.RemoveBox(id) }

func (b *Boxes) CacheHost(ctx context.Context, name contract.HostName) (enginegit.CacheHost, string, error) {
	h, err := b.host(ctx, name)
	return h, b.Layout.HostData, err
}

func (b *Boxes) AddHost(ctx context.Context, req contract.HostAddRequest) (bool, error) {
	h, err := podman.New(req.SSH, b.Runtime, string(req.Name), b.hostRunner)
	if err != nil {
		return false, err
	}
	check := h.Check(ctx, podman.CheckOptions{HostData: b.Layout.HostData, Runtime: b.Runtime, AdminOnly: req.OwnerOnlyAdmin})
	if !check.Ready() {
		for _, item := range check.Items {
			if !item.OK {
				if item.Name == podman.CheckPodman {
					return false, podman.RequiredError{}
				}
				return false, fmt.Errorf("host check %s: %s", item.Name, item.Reason)
			}
		}
		return false, fmt.Errorf("host check failed")
	}
	if err := b.installBinary(ctx, h); err != nil {
		return false, err
	}
	if err := b.syncInstructions(ctx, h); err != nil {
		return false, err
	}
	same, err := b.engineHost(ctx, h)
	if err != nil {
		return false, err
	}
	record := box.Host{Rooms: req.Rooms, EngineHost: &same, Name: req.Name, SSH: req.SSH, State: box.HostReady, CPUs: check.CPUs, MemBytes: check.MemBytes, DiskBytes: check.DiskBytes, CheckedAt: time.Now()}
	if err := b.Store.AddHost(ctx, record); err != nil {
		return false, err
	}
	b.mu.Lock()
	if b.loads == nil {
		b.loads = make(map[contract.HostName]hostLoad)
	}
	b.loads[req.Name] = hostLoad{summary: contract.HostSummary{Reachable: true}}
	b.cache(req.Name, h)
	b.mu.Unlock()
	return check.NoSwap, nil
}

func (b *Boxes) forward(id contract.BoxID) box.Forward {
	return box.Forward{Box: id, HostPath: filepath.Join(b.Layout.HostData, "sock", string(id), "engine.sock"), EnginePath: filepath.Join(b.Runtime, "boxes", string(id)+".sock")}
}

func (b *Boxes) Create(ctx context.Context, spec lifecycle.BoxSpec) (string, error) {
	h, err := b.host(ctx, spec.Host)
	if err != nil {
		return "", err
	}
	if err := b.syncInstructions(ctx, h); err != nil {
		return "", err
	}
	session, err := b.Store.Session(ctx, spec.Session)
	if err != nil {
		return "", err
	}
	if err := b.Sockets.AddBox(spec.Box, spec.Session, session.Room); err != nil {
		return "", err
	}
	ok := false
	var forwarded, sliced, created bool
	forward := b.forward(spec.Box)
	defer func() {
		if ok {
			return
		}
		if created {
			_ = h.RemoveContainer(context.Background(), spec.Box)
			_ = h.RemoveVolume(context.Background(), spec.Box)
		}
		if forwarded {
			_ = h.RemoveForward(context.Background(), forward)
		}
		if sliced {
			_ = h.RemoveSlice(context.Background(), spec.Slice)
		}
		_ = b.Sockets.RemoveBox(spec.Box)
	}()
	if err = b.installBinary(ctx, h); err != nil {
		return "", err
	}
	image, err := b.CurrentImage(ctx, spec.Session, spec.Host)
	if err != nil {
		return "", err
	}
	spec.Needs = image.Needs
	slice, c := b.container(spec, image.Tag)
	if err = h.EnsureSlice(ctx, slice); err != nil {
		return "", err
	}
	sliced = true
	if err = h.AddForward(ctx, forward); err != nil {
		return "", err
	}
	forwarded = true
	if err = h.CreateContainer(ctx, c); err != nil {
		var missing box.MissingDevice
		if errors.As(err, &missing) {
			missing.Host = string(spec.Host)
			return "", missing
		}
		return "", err
	}
	created = true
	if err = h.StartContainer(ctx, spec.Box); err != nil {
		return "", err
	}
	ok = true
	return image.Tag, nil
}

// Restore creates local sockets first and attempts each host independently.
func (b *Boxes) Restore(ctx context.Context) error {
	records, err := b.Store.Boxes(ctx)
	if err != nil {
		return err
	}
	hosts := make(map[contract.HostName]bool)
	for _, record := range records {
		if record.Host == "" || record.State == contract.BoxDeleted || record.State == contract.BoxFailed {
			continue
		}
		session, err := b.Store.Session(ctx, record.Session)
		if err != nil {
			return err
		}
		if err := b.Sockets.AddBox(record.ID, record.Session, session.Room); err != nil {
			return err
		}
		hosts[record.Host] = true
	}
	var wg sync.WaitGroup
	for name := range hosts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			hostCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			if err := b.restoreHost(hostCtx, name); err != nil {
				log.Printf("host %s: restore: %v", name, err)
			}
		}()
	}
	wg.Wait()
	return ctx.Err()
}

func (b *Boxes) restoreHost(ctx context.Context, name contract.HostName) error {
	b.mu.Lock()
	restored := b.restored[name]
	b.mu.Unlock()
	if restored {
		return nil
	}
	host, err := b.host(ctx, name)
	if err != nil {
		return err
	}
	if err := b.installBinary(ctx, host); err != nil {
		return err
	}
	records, err := b.Store.Boxes(ctx)
	if err != nil {
		return err
	}
	for _, record := range records {
		if record.Host != name || record.State == contract.BoxDeleted || record.State == contract.BoxFailed {
			continue
		}
		forward := b.forward(record.ID)
		if err := host.AddForward(ctx, forward); err != nil {
			return err
		}
	}
	b.mu.Lock()
	if b.restored == nil {
		b.restored = make(map[contract.HostName]bool)
	}
	b.restored[name] = true
	b.mu.Unlock()
	return nil
}

func (b *Boxes) containerHost(ctx context.Context, id contract.BoxID) (*podman.Host, error) {
	record, err := b.Store.Box(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := b.restoreHost(ctx, record.Host); err != nil {
		return nil, err
	}
	return b.host(ctx, record.Host)
}

func (b *Boxes) Inspect(ctx context.Context, id contract.BoxID) (box.ContainerStatus, bool, error) {
	host, err := b.containerHost(ctx, id)
	if err != nil {
		return box.ContainerStatus{}, false, err
	}
	return inspectContainer(ctx, host, id)
}

func inspectContainer(ctx context.Context, host box.HostDriver, id contract.BoxID) (box.ContainerStatus, bool, error) {
	statuses, err := host.ListContainers(ctx)
	if err != nil {
		return box.ContainerStatus{}, false, err
	}
	for _, status := range statuses {
		if status.Box == id {
			return status, true, nil
		}
	}
	return box.ContainerStatus{}, false, nil
}

func (b *Boxes) Start(ctx context.Context, id contract.BoxID) error {
	host, err := b.containerHost(ctx, id)
	if err != nil {
		return err
	}
	if err := b.syncInstructions(ctx, host); err != nil {
		return err
	}
	return host.StartContainer(ctx, id)
}

func (b *Boxes) Stop(ctx context.Context, id contract.BoxID) error {
	host, err := b.containerHost(ctx, id)
	if err != nil {
		return err
	}
	return host.StopContainer(ctx, id)
}

func (b *Boxes) Remove(ctx context.Context, id contract.BoxID) error {
	record, err := b.Store.Box(ctx, id)
	if err != nil {
		return err
	}
	if record.Host == "" {
		return nil
	}
	h, err := b.host(ctx, record.Host)
	if err != nil {
		return err
	}
	forward := b.forward(id)
	if err := removeHostBox(ctx, h, record, forward); err != nil {
		return err
	}
	return b.Sockets.RemoveBox(id)
}

// removeHostBox cleans up every host resource, including a partial creation.
func removeHostBox(ctx context.Context, h box.HostDriver, record box.Box, forward box.Forward) error {
	if err := h.StopContainer(ctx, record.ID); err != nil {
		return err
	}
	if err := h.RemoveForward(ctx, forward); err != nil {
		return err
	}
	if err := h.RemoveContainer(ctx, record.ID); err != nil {
		return err
	}
	if err := h.RemoveVolume(ctx, record.ID); err != nil {
		return err
	}
	return h.RemoveSlice(ctx, record.Slice)
}

type scopeRunner interface {
	RunHostexec(context.Context, contract.HostexecRunArgs, contract.OpID) ([]byte, int, bool, error)
	StopHostexec(context.Context, string) error
}

func (b *Boxes) TerminalArgv(ctx context.Context, host contract.HostName, id contract.BoxID) ([]string, error) {
	if host == contract.ControlPlane && id == "" {
		shell := os.Getenv("SHELL")
		if shell == "" {
			shell = "/bin/sh"
		}
		return []string{"systemd-run", "--user", "--scope", "--quiet", "--", shell, "-l"}, nil
	}
	h, err := b.host(ctx, host)
	if err != nil {
		return nil, err
	}
	return h.TerminalArgv(id), nil
}

func (b *Boxes) scopeRunner(ctx context.Context, host contract.HostName) (scopeRunner, error) {
	if host == contract.ControlPlane {
		return hostexec.Local{}, nil
	}
	return b.host(ctx, host)
}
func (b *Boxes) RunHostexec(ctx context.Context, a contract.HostexecRunArgs, op contract.OpID) ([]byte, int, bool, error) {
	r, err := b.scopeRunner(ctx, a.Host)
	if err != nil {
		return nil, 0, false, err
	}
	return r.RunHostexec(ctx, a, op)
}
func (b *Boxes) CurrentImage(ctx context.Context, sessionID contract.SessionID, host contract.HostName) (box.Image, error) {
	h, err := b.host(ctx, host)
	if err != nil {
		return box.Image{}, err
	}
	session, err := b.Store.Session(ctx, sessionID)
	if err != nil {
		return box.Image{}, err
	}
	tag, err := h.BuildBase(ctx, images.BaseContainerfile, b.PiTarball)
	if err != nil {
		return box.Image{}, err
	}
	var needs []box.Need
	if session.Repo != "" {
		name, tree, data, imageNeeds, err := b.Mirror.ImageContext(ctx, session.Repo)
		if err != nil {
			return box.Image{}, err
		}
		needs = imageNeeds
		if name != "" {
			tag, err = b.Images.Build(ctx, h, session.Room, host, session.Repo, name, tag, tree, data)
			if err != nil {
				return box.Image{}, err
			}
		}
	}
	return box.Image{Tag: tag, Needs: needs}, nil
}

func (b *Boxes) StopHostexec(ctx context.Context, host contract.HostName, scope string) error {
	r, err := b.scopeRunner(ctx, host)
	if err != nil {
		return err
	}
	return r.StopHostexec(ctx, scope)
}

// Recreate replaces a container without touching its volume or a legacy parent slice.
func (b *Boxes) Recreate(ctx context.Context, spec lifecycle.BoxSpec, image, _ string) error {
	if err := b.restoreHost(ctx, spec.Host); err != nil {
		return err
	}
	h, err := b.host(ctx, spec.Host)
	if err != nil {
		return err
	}
	if err := b.syncInstructions(ctx, h); err != nil {
		return err
	}
	if err := h.StopContainer(ctx, spec.Box); err != nil {
		return err
	}
	if err := h.RemoveContainer(ctx, spec.Box); err != nil {
		return err
	}
	slice, c := b.container(spec, image)
	if err := h.EnsureSlice(ctx, slice); err != nil {
		return err
	}
	if err := h.CreateContainer(ctx, c); err != nil {
		var missing box.MissingDevice
		if errors.As(err, &missing) {
			missing.Host = string(spec.Host)
			return missing
		}
		return err
	}
	return h.StartContainer(ctx, spec.Box)
}

func (b *Boxes) container(spec lifecycle.BoxSpec, image string) (box.Slice, box.ContainerSpec) {
	size := spec.Size
	slice := box.Slice{Name: spec.Slice, CPUs: size.CPUs, MemHigh: size.MemBytes, MemMax: size.MemBytes * 3 / 2, Pids: size.Pids}
	c := box.ContainerSpec{Box: spec.Box, Image: image, Slice: spec.Slice, Needs: spec.Needs, Labels: []box.Label{{Key: "dyna.layout", Value: box.ContainerLayout}}, Mounts: []box.Mount{
		{HostPath: filepath.Join(b.Layout.HostData, "sock", string(spec.Box)), BoxPath: "/run/dyna/engine", ReadOnly: true},
		{HostPath: filepath.Join(b.Layout.HostData, "bin"), BoxPath: "/opt/dyna/bin", ReadOnly: true},
		{HostPath: filepath.Join(b.Layout.HostData, "cache"), BoxPath: "/box/cache"},
		{HostPath: b.Layout.HostShared(spec.Room), BoxPath: "/box/shared"},
		{HostPath: filepath.Dir(b.Layout.HostInstructions("")), BoxPath: "/run/dyna/instructions/global", ReadOnly: true, MakeDir: true},
		{HostPath: filepath.Dir(b.Layout.HostInstructions(spec.Room)), BoxPath: "/run/dyna/instructions/room", ReadOnly: true, MakeDir: true},
	}, Env: []box.EnvVar{{Name: "GOMODCACHE", Value: "/box/cache/gomod"}, {Name: "GOCACHE", Value: "/box/cache/gocache"}, {Name: "CARGO_HOME", Value: "/box/cache/cargo"}, {Name: "npm_config_store_dir", Value: "/box/cache/pnpm"}}}
	for _, repo := range spec.Repos {
		c.Mounts = append(c.Mounts, box.Mount{HostPath: repo.Cache, BoxPath: repo.Cache, ReadOnly: true})
	}
	return slice, c
}

func (b *Boxes) engineHost(ctx context.Context, h *podman.Host) (bool, error) {
	path := b.machineIDFile
	if path == "" {
		path = "/etc/machine-id"
	}
	local, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	id := strings.TrimSpace(string(local))
	if len(id) != 32 || strings.Trim(id, "0123456789abcdef") != "" {
		return false, fmt.Errorf("invalid engine machine-id")
	}
	remote, err := h.MachineID(ctx)
	return id == remote, err
}
