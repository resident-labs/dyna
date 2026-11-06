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
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/box/images"
	"github.com/resident-labs/dyna/box/podman"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine"
	"github.com/resident-labs/dyna/engine/addons"
	"github.com/resident-labs/dyna/engine/api"
	"github.com/resident-labs/dyna/engine/ci"
	"github.com/resident-labs/dyna/engine/compose"
	enginegit "github.com/resident-labs/dyna/engine/git"
	"github.com/resident-labs/dyna/engine/hostexec"
	engineimages "github.com/resident-labs/dyna/engine/images"
	"github.com/resident-labs/dyna/engine/lifecycle"
	"github.com/resident-labs/dyna/engine/store"
)

type boxStore interface {
	Hosts(context.Context) ([]box.Host, error)
	AddHost(context.Context, box.Host) error
	SetHostChecked(context.Context, box.Host) error
	SetRoomShared(context.Context, []contract.RoomShared, time.Time) error
	SetEngineHost(context.Context, contract.HostName, bool) error
	Session(context.Context, contract.SessionID) (store.Session, error)
	Box(context.Context, contract.BoxID) (box.Box, error)
	Boxes(context.Context) ([]box.Box, error)
	Services(context.Context, contract.RoomID) ([]store.ServiceRow, error)
	OpenRooms(context.Context) ([]contract.RoomID, error)
	RoomRepos(context.Context, contract.RoomID) ([]contract.RoomRepo, error)
	ActiveRuns(context.Context) ([]store.HookRun, error)
	PinWorkflowImage(context.Context, contract.SessionID, string) (string, error)
}

type heartbeatStore interface {
	RecordHostHeartbeat(context.Context, contract.HostName, bool, *int64, int64, time.Time) error
	SetHostGPUs(context.Context, contract.HostName, []contract.GPU) error
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
	instructionSums   map[contract.RoomID][sha256.Size]byte // each AGENTS.md as last noted, under instructionsMu
	Version           string
	Mirror            *enginegit.Mirror
	Images            *engineimages.Builder
	RoomImages        *engineimages.Store
	Migrator          *engineimages.Migrator
	Compose           *compose.Files
	Addons            *addons.Files
	Context           context.Context
	Pressure          *engine.Service
	Proc              string // Proc is the folder with stat and meminfo; empty means /proc.
	mu                sync.Mutex
	hosts             map[contract.HostName]*podman.Host
	restored          map[contract.HostName]bool   // forwards back in this process
	installed         map[contract.HostName]bool   // binary current in this process
	shareProblems     map[contract.HostName]string // the last reason each host's mount could not come up
	recoveries        map[contract.HostName]*recovery
	recovering        sync.WaitGroup
	retryDelay        time.Duration // retryDelay is the first wait between recovery tries; zero means restoreRetryMin.
	loads             map[contract.HostName]hostLoad
	beats             map[contract.HostName]context.CancelFunc // each host's heartbeat
	cpuPrev           *cpuTimes                                // cpuPrev is the last /proc/stat sample of this machine, nil before the first.
	installs          map[*podman.Host]*binaryInstall
	starts            map[contract.BoxID]contract.BoxStartStatus
	stepContext       func(context.Context, time.Duration) (context.Context, context.CancelFunc)
	binCacheOnce      sync.Once
	binCache          *podman.BinaryCache
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

// installBinary shares one progress-aware operation per host, with subscriber-owned cancellation.
func (b *Boxes) installBinary(ctx context.Context, h *podman.Host) error {
	if _, err := b.binarySum(); err != nil {
		return err
	}
	b.mu.Lock()
	if b.installs == nil {
		b.installs = make(map[*podman.Host]*binaryInstall)
	}
	job := b.installs[h]
	if job != nil && job.closing {
		b.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-job.done:
		}
		b.mu.Lock()
		if b.installs[h] == job {
			delete(b.installs, h)
		}
		b.mu.Unlock()
		return b.installBinary(ctx, h)
	}
	if job == nil {
		parent := b.Context
		if parent == nil {
			parent = context.WithoutCancel(ctx)
		}
		work, cancel := context.WithCancel(parent)
		job = &binaryInstall{done: make(chan struct{}), cancel: cancel, progress: make(chan struct{}, 1)}
		b.installs[h] = job
		go func() {
			idle, stop := podman.IdleContext(work, box.StartStepBudget, job.progress)
			defer stop()
			progress := podman.WithByteProgress(idle, func(n int) {
				if n > 0 {
					select {
					case job.progress <- struct{}{}:
					default:
					}
				}
			})
			job.err = h.InstallBinary(progress, b.Layout.HostBin(), b.Version, b.binSum, b.compressedBinary)
			if errors.Is(context.Cause(idle), podman.ErrTransferIdle) {
				job.err = errors.Join(podman.ErrTransferIdle, job.err)
			}
			close(job.done)
		}()
	}
	job.refs++
	b.mu.Unlock()
	var err error
	select {
	case <-job.done:
		err = job.err
	case <-ctx.Done():
		err = ctx.Err()
	}
	b.mu.Lock()
	job.refs--
	last := job.refs == 0
	if last {
		job.closing = true
		job.cancel()
	}
	b.mu.Unlock()
	if last {
		<-job.done
		b.mu.Lock()
		if b.installs[h] == job {
			delete(b.installs, h)
		}
		b.mu.Unlock()
	}
	return err
}

func (b *Boxes) compressedBinary(encoding podman.BinaryEncoding) ([]byte, error) {
	b.binCacheOnce.Do(func() { b.binCache = podman.NewBinaryCache(readExecutable) })
	return b.binCache.Bytes(encoding)
}

func (b *Boxes) binarySum() (string, error) {
	b.binOnce.Do(func() { b.binSum, b.binErr = executableSum() })
	return b.binSum, b.binErr
}

// startBuildCache starts the host's compiler cache for a box or runner about to start there. A build without it compiles every
// crate itself, so a failure is only logged.
func (b *Boxes) startBuildCache(ctx context.Context, name contract.HostName, h *podman.Host) {
	if err := h.EnsureBuildCache(ctx, b.Layout); err != nil && ctx.Err() == nil {
		log.Printf("host %s: build cache: %v", name, err)
	}
}

// ciHost gives the CI pool a host's runner operations.
type ciHost struct {
	*podman.Host
	name  contract.HostName
	boxes *Boxes
}

// EnsureEgress installs the engine binary first, because the proxy is that binary.
func (h ciHost) EnsureEgress(ctx context.Context, image string) error {
	if err := h.boxes.installBinary(ctx, h.Host); err != nil {
		return err
	}
	return h.Host.EnsureEgress(ctx, image, h.boxes.Layout.HostBin())
}

// StartRunner gives the runner the host's cache, the one its boxes mount at /box/cache, and their compiler cache.
func (h ciHost) StartRunner(ctx context.Context, spec box.RunnerSpec) error {
	h.boxes.startBuildCache(ctx, h.name, h.Host)
	spec.Cache, spec.BuildCache = h.boxes.Layout.HostCache(), h.boxes.Layout.HostBuildCacheSocketFolder()
	return h.Host.StartRunner(ctx, spec)
}

func (b *Boxes) ciDriver(ctx context.Context, name contract.HostName) (ci.Driver, error) {
	h, err := b.host(ctx, name)
	if err != nil {
		return nil, err
	}
	return ciHost{Host: h, name: name, boxes: b}, nil
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
	if b.beats == nil {
		b.beats = make(map[contract.HostName]context.CancelFunc)
	}
	ctx, stop := context.WithCancel(b.Context)
	b.beats[name] = stop
	// The heartbeat asks each beat, since host set can turn an exec-only host into a full one. A store error keeps the last answer.
	last := false
	h.SetExecOnly(func() bool {
		hosts, err := b.Store.Hosts(ctx)
		if err != nil {
			log.Printf("host %s: exec-only check: %v", name, err)
			return last
		}
		last = slices.ContainsFunc(hosts, func(record box.Host) bool { return record.Name == name && record.ExecOnly })
		return last
	})
	h.SetServices(func() []contract.ServiceID { return b.Pressure.Reconciler().ServiceIDs(ctx, name) })
	ticker := time.NewTicker(10 * time.Second)
	go func() {
		defer ticker.Stop()
		h.Heartbeat(ctx, ticker.C, b.Layout, func(ok bool, load podman.Load) {
			now := time.Now()
			if ok && load.Probe == podman.ProbeGood && !load.ExecOnly {
				if err := b.syncInstructions(ctx, h); err != nil {
					log.Printf("host %s: instructions: %v", name, err)
				}
			}
			b.record(name, ok, load, now)
			if ok {
				b.Pressure.Reconciler().ObserveServices(load.Services)
			}
			b.reportHeartbeat(ctx, name, ok, load, now)
			if ok && load.Probe == podman.ProbeGood && !load.ExecOnly && !load.ShareUp && b.recovered(name) {
				b.repairShare(ctx, name)
			}
		}, b.pressure(name))
	}()
}

// forgetHost drops the engine's side of a host that is leaving: its heartbeat, its connection, its forwards and what this
// process knows of it. The name can then come back as a new host.
func (b *Boxes) forgetHost(ctx context.Context, name contract.HostName) {
	b.mu.Lock()
	h := b.hosts[name]
	if stop := b.beats[name]; stop != nil {
		stop()
	}
	delete(b.beats, name)
	delete(b.hosts, name)
	delete(b.restored, name)
	delete(b.installed, name)
	delete(b.shareProblems, name)
	delete(b.recoveries, name)
	delete(b.loads, name)
	delete(b.installs, h)
	b.mu.Unlock()
	if h == nil {
		return
	}
	b.instructionsMu.Lock()
	delete(b.instructionSynced, h)
	b.instructionsMu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	h.Close(ctx)
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
			// Nothing here touches the network: a slow host must not hold up a caller that wants another one.
			h, err := podman.New(record.SSH, b.Runtime, string(name), nil)
			if err != nil {
				return nil, err
			}
			if record.Pool != "" {
				h.TrustNewKey()
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

func (b *Boxes) MaintainHost(ctx context.Context, name contract.HostName) error {
	h, err := b.host(ctx, name)
	if err != nil {
		return err
	}
	b.prepareImages(ctx, h, name)
	b.sweepRuns(ctx, name, h)
	return h.Maintain(ctx, b.Layout)
}

// BuildAhead prepares every ready host's images when the engine starts, which is when a deploy changes dyna-base.
// Hosts go side by side, and each builds one image at a time, so running boxes keep the rest of their host.
func (b *Boxes) BuildAhead(ctx context.Context) {
	hosts, err := b.Store.Hosts(ctx)
	if err != nil {
		log.Printf("build ahead: list hosts: %v", err)
		return
	}
	var wg sync.WaitGroup
	for _, record := range hosts {
		if record.State != box.HostReady || record.ExecOnly {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			h, err := b.host(ctx, record.Name)
			if err != nil {
				log.Printf("host %s: build ahead: %v", record.Name, err)
				return
			}
			b.prepareImages(ctx, h, record.Name)
		}()
	}
	wg.Wait()
}

// prepareImages builds the images that boxes on this host would use now, then prunes older ones. A failure is only
// logged: the next box that needs the image builds it and reports the failure in its room.
func (b *Boxes) prepareImages(ctx context.Context, h *podman.Host, name contract.HostName) {
	// Running boxes come first: until a host is back, image work would only compete with them for its link.
	if !b.waitRecovered(ctx, name) {
		return
	}
	steps := box.NewStepLog("host " + string(name))
	rooms, err := b.hostRepos(ctx, name)
	if err != nil {
		log.Printf("host %s: build ahead: %v", name, err)
		return
	}
	image, err := b.image(ctx, h, name, "", "", "", contract.BoxSize{})
	if err != nil {
		log.Printf("host %s: build ahead: %v", name, err)
		return
	}
	keep := []string{image.Tag}
	for _, pair := range rooms {
		image, err := b.image(ctx, h, name, pair.room, pair.repo, "", contract.BoxSize{})
		if err != nil {
			log.Printf("host %s: build ahead %s %s: %v", name, pair.room, pair.repo, err)
			continue
		}
		keep = append(keep, image.Tag)
	}
	keep = append(keep, b.prepareCompose(ctx, h, name, rooms)...)
	// A service's running image and the one before it stay: a rollback needs the previous one, and box images share the families
	// that pruning thins to the newest two.
	kept, err := b.serviceImages(ctx, name)
	if err != nil {
		log.Printf("host %s: build ahead: services: %v", name, err)
		return
	}
	keep = append(keep, kept...)
	steps.Done("build ahead")
	if err := h.PruneImages(ctx, keep); err != nil {
		log.Printf("host %s: prune images: %v", name, err)
	}
	steps.Done("prune images")
}

// serviceImages lists the image references the services on a host run or can roll back to.
func (b *Boxes) serviceImages(ctx context.Context, host contract.HostName) ([]string, error) {
	services, err := b.Store.Services(ctx, "")
	var refs []string
	for _, row := range services {
		if row.Host == host {
			refs = append(refs, row.ImageRef, row.Previous)
		}
	}
	return refs, err
}

// prepareCompose builds and pulls what the compose files in reach of this host's rooms name, and returns the tags. A file
// that fails is only logged, as an image is.
func (b *Boxes) prepareCompose(ctx context.Context, h *podman.Host, name contract.HostName, rooms []roomRepo) []string {
	if b.Compose == nil {
		return nil
	}
	var keep []string
	for _, pair := range rooms {
		names, err := b.Compose.Names(ctx, pair.room, pair.repo)
		if err != nil {
			log.Printf("host %s: build ahead compose in %s: %v", name, pair.room, err)
			continue
		}
		for _, file := range names {
			image, err := b.image(ctx, h, name, pair.room, pair.repo, file, contract.BoxSize{})
			if err != nil {
				log.Printf("host %s: build ahead compose %s in %s: %v", name, file, pair.room, err)
				continue
			}
			keep = append(keep, image.Tags()...)
		}
	}
	return keep
}

// roomRepo is one place a session can start: a room, with one of its repositories or with none.
type roomRepo struct {
	room contract.RoomID
	repo contract.RepoName
}

// hostRepos returns every room and repository pair a room on this host can use, and each room once with no repository,
// because images and compose files are the room's.
func (b *Boxes) hostRepos(ctx context.Context, name contract.HostName) ([]roomRepo, error) {
	hosts, err := b.Store.Hosts(ctx)
	if err != nil {
		return nil, err
	}
	var host box.Host
	for _, record := range hosts {
		if record.Name == name {
			host = record
		}
	}
	rooms, err := b.Store.OpenRooms(ctx)
	if err != nil {
		return nil, err
	}
	var pairs []roomRepo
	for _, room := range rooms {
		// A host with agents off serves no room, so it builds no room's images.
		if !host.Serves(room) {
			continue
		}
		listed, err := b.Store.RoomRepos(ctx, room)
		if err != nil {
			return nil, err
		}
		pairs = append(pairs, roomRepo{room: room})
		for _, repo := range listed {
			pairs = append(pairs, roomRepo{room, repo.Repo})
		}
	}
	return pairs, nil
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
	if err := b.instructionState(h).settle(ctx); err != nil {
		return err
	}
	return h.RemoveRoom(podman.WithRemoteCancellation(ctx), dir)
}

// RemoveLeftoverBox removes a box that an offline host still held when its room was deleted. The box has no row any more.
func (b *Boxes) RemoveLeftoverBox(ctx context.Context, name contract.HostName, id contract.BoxID, slice string) error {
	h, err := b.host(ctx, name)
	if err != nil {
		return err
	}
	return removeHostBox(podman.WithRemoteCancellation(ctx), h, box.Box{ID: id, Slice: slice}, b.forward(id))
}

// ForgetBox closes the engine's socket for a box that its offline host still holds.
func (b *Boxes) ForgetBox(id contract.BoxID) error { return b.Sockets.RemoveBox(id) }

func (b *Boxes) CacheHost(ctx context.Context, name contract.HostName) (enginegit.CacheHost, error) {
	return b.host(ctx, name)
}

func (b *Boxes) AddHost(ctx context.Context, req contract.HostAddRequest) (bool, error) {
	h, err := podman.New(req.SSH, b.Runtime, string(req.Name), b.hostRunner)
	if err != nil {
		return false, err
	}
	// An exec-only host needs SSH and bash. Nothing is installed there and no box or runner is ever placed there.
	if req.ExecOnly() {
		if err := h.CheckExec(ctx); err != nil {
			return false, fmt.Errorf("host check %s: %w", podman.CheckExec, err)
		}
		notEngine := false
		record := box.Host{EngineHost: &notEngine, Name: req.Name, SSH: req.SSH, State: box.HostReady, NoAgents: true, ExecOnly: true, Rooms: req.Rooms, CheckedAt: time.Now()}
		if err := b.Store.AddHost(ctx, record); err != nil {
			return false, err
		}
		b.adopt(req.Name, h)
		return false, nil
	}
	record, noSwap, err := b.setUpHost(ctx, h, req.Name, req.SSH, req.OwnerOnlyAdmin)
	if err != nil {
		return false, err
	}
	record.Rooms, record.NoAgents, record.CI = req.Rooms, req.Agents != nil && !*req.Agents, req.CIOn(*record.EngineHost)
	if err := b.Store.AddHost(ctx, record); err != nil {
		return false, err
	}
	b.adopt(req.Name, h)
	return noSwap, nil
}

// joinHost brings a pool's new machine in the way AddHost brings in the owner's, and leaves recording it to the pool step.
func (b *Boxes) joinHost(ctx context.Context, name contract.HostName, ssh string) (box.Host, error) {
	h, err := podman.New(ssh, b.Runtime, string(name), b.hostRunner)
	if err != nil {
		return box.Host{}, err
	}
	h.TrustNewKey()
	record, _, err := b.setUpHost(ctx, h, name, ssh, true)
	if err != nil {
		return box.Host{}, err
	}
	b.adopt(name, h)
	return record, nil
}

// setUpHost runs the host check and installs what a host needs, and reports whether it has no swap.
func (b *Boxes) setUpHost(ctx context.Context, h *podman.Host, name contract.HostName, ssh string, ownerOnly bool) (box.Host, bool, error) {
	same, err := b.engineHost(ctx, h)
	if err != nil {
		return box.Host{}, false, err
	}
	check := h.Check(ctx, podman.CheckOptions{Layout: b.Layout, Runtime: b.Runtime, AdminOnly: ownerOnly, EngineHost: same})
	// A host without its shared-folder mount still serves CI runners, which do not use the folder, so it is ready but takes no
	// session box: the create quarantine says so.
	var problem contract.HostProblem
	for i, item := range check.Items {
		if !item.OK && item.Name == podman.CheckShare {
			log.Printf("host %s: no session boxes: %s: %s", name, item.Name, item.Reason)
			problem = contract.HostProblemShareUnavailable
			check.Items[i].OK = true
		}
	}
	if !check.Ready() {
		for _, item := range check.Items {
			if !item.OK {
				if item.Name == podman.CheckPodman {
					return box.Host{}, false, podman.RequiredError{}
				}
				return box.Host{}, false, fmt.Errorf("host check %s: %s", item.Name, item.Reason)
			}
		}
		return box.Host{}, false, fmt.Errorf("host check failed")
	}
	if err := b.installBinary(ctx, h); err != nil {
		return box.Host{}, false, err
	}
	if err := b.syncInstructions(ctx, h); err != nil {
		return box.Host{}, false, err
	}
	return box.Host{EngineHost: &same, Name: name, SSH: ssh, State: box.HostReady, Problem: problem, CPUs: check.CPUs, MemBytes: check.MemBytes, DiskBytes: check.DiskBytes, CheckedAt: time.Now()}, check.NoSwap, nil
}

// adopt starts a recorded host's heartbeat.
func (b *Boxes) adopt(name contract.HostName, h *podman.Host) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.loads == nil {
		b.loads = make(map[contract.HostName]hostLoad)
	}
	b.loads[name] = hostLoad{summary: contract.HostSummary{Reachable: true}}
	b.cache(name, h)
}

// poolHosts is what the pool step needs: the pool's script, and this engine's side of a machine.
type poolHosts struct {
	lifecycle.Script
	boxes *Boxes
}

func (p poolHosts) Join(ctx context.Context, name contract.HostName, ssh string) (box.Host, error) {
	return p.boxes.joinHost(ctx, name, ssh)
}

func (p poolHosts) Leave(ctx context.Context, name contract.HostName) { p.boxes.forgetHost(ctx, name) }

func (b *Boxes) forward(id contract.BoxID) box.Forward {
	return box.Forward{Box: id, HostPath: b.Layout.HostSocket(id), EnginePath: filepath.Join(b.Runtime, "boxes", string(id)+".sock")}
}

func (b *Boxes) Create(ctx context.Context, spec lifecycle.BoxSpec) (string, error) {
	defer func() { b.mu.Lock(); delete(b.starts, spec.Box); b.mu.Unlock() }()
	run := func(step contract.StartStep, work func(context.Context) error) error {
		return b.startStep(ctx, spec, step, work)
	}
	steps := box.NewStepLog("box " + string(spec.Box))
	h, err := b.host(ctx, spec.Host)
	if err != nil {
		return "", err
	}
	if err := run(contract.StartInstructions, func(ctx context.Context) error { return b.syncInstructions(ctx, h) }); err != nil {
		return "", err
	}
	steps.Done("instructions")
	session, err := b.Store.Session(ctx, spec.Session)
	if err != nil {
		return "", err
	}
	spec.Root = session.Root
	if err := b.Sockets.AddBox(spec.Box, spec.Session, session.Room); err != nil {
		return "", box.RuntimeCreateError{Err: err}
	}
	ok := false
	var forwarded, sliced, created, stacked bool
	forward := b.forward(spec.Box)
	defer func() {
		if ok {
			return
		}
		cleanupCreate(ctx, h, spec, forward, stacked, created, forwarded, sliced)
		_ = b.Sockets.RemoveBox(spec.Box)
	}()
	if err = run(contract.StartBinary, func(ctx context.Context) error { return b.installBinary(ctx, h) }); err != nil {
		return "", err
	}
	steps.Done("binary")
	if err = run(contract.StartBuildCache, func(ctx context.Context) error { b.startBuildCache(ctx, spec.Host, h); return nil }); err != nil {
		return "", err
	}
	steps.Done("build cache")
	var shared box.Mount
	err = run(contract.StartShared, func(ctx context.Context) error {
		var e error
		shared, e = b.sharedMount(ctx, spec.Host, spec.Room)
		return e
	})
	if err != nil {
		return "", err
	}
	steps.Done("shared folder")
	var image box.Image
	err = run(contract.StartImage, func(ctx context.Context) error {
		var e error
		image, e = b.CurrentImage(ctx, spec.Session, spec.Host)
		return e
	})
	if err != nil {
		return "", err
	}
	steps.Done("image")
	spec.Needs, spec.Stack = image.Needs, image.Stack
	slice, c := b.container(spec, image.Tag, shared)
	sliced = true
	if err = run(contract.StartSlice, func(ctx context.Context) error { return h.EnsureSlice(ctx, slice) }); err != nil {
		return "", box.RuntimeCreateError{Err: err}
	}
	sliced = true
	steps.Done("slice")
	forwarded = true
	if err = run(contract.StartForward, func(ctx context.Context) error { return h.AddForward(ctx, forward) }); err != nil {
		return "", box.RuntimeCreateError{Err: err}
	}
	forwarded = true
	steps.Done("forward")
	if spec.Stack != nil {
		stacked = true
		if err = run(contract.StartStack, func(ctx context.Context) error { return h.CreateStack(ctx, spec.Box, spec.Slice, spec.Stack) }); err != nil {
			return "", err
		}
		steps.Done("compose stack")
	}
	created = true
	if err = run(contract.StartCreate, func(ctx context.Context) error { return h.CreateContainer(ctx, c) }); err != nil {
		var missing box.MissingDevice
		if errors.As(err, &missing) {
			missing.Host = string(spec.Host)
			return "", missing
		}
		var imageErr box.ImageCreateError
		if errors.As(err, &imageErr) {
			return "", err
		}
		return "", box.RuntimeCreateError{Err: err}
	}
	created = true
	steps.Done("container create")
	if spec.Stack != nil {
		if err = run(contract.StartSidecars, func(ctx context.Context) error { return h.StartSidecars(ctx, spec.Box) }); err != nil {
			return "", err
		}
		steps.Done("sidecars")
	}
	if err = run(contract.StartContainer, func(ctx context.Context) error { return h.StartContainer(ctx, spec.Box) }); err != nil {
		return "", box.RuntimeCreateError{Err: err}
	}
	steps.Done("container start")
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	ok = true
	return image.Tag, nil
}

// cleanupCreate removes what a failed creation made, on its own bounded context: the creation context is often the reason it failed.
func cleanupCreate(ctx context.Context, h box.HostDriver, spec lifecycle.BoxSpec, forward box.Forward, stacked, created, forwarded, sliced bool) {
	ctx, cancel := startCleanupContext(ctx)
	defer cancel()
	if stacked {
		_ = h.RemovePod(ctx, spec.Box)
	}
	if created {
		_ = h.RemoveContainer(ctx, spec.Box)
	}
	if forwarded {
		_ = h.RemoveForward(ctx, forward)
	}
	if sliced {
		_ = h.RemoveSlice(ctx, spec.Slice)
	}
}

// recovery is one host's way back after the engine starts.
type recovery struct {
	done chan struct{} // closes when the host has its forwards and this binary, or the engine stops.
	hold time.Time     // Until then the reconciler does not count a box's silence against it. mu guards it.
}

const (
	// restoreTry bounds one pass over a host's forwards, so a stuck ssh cannot end recovery. Each forward stays in the driver,
	// so the next pass carries on from there and even a very slow host gets there.
	restoreTry = 2 * time.Minute
	// restoreHold ends the wait on a host that answers but keeps refusing its forwards: its boxes show as lost, and the
	// retries go on.
	restoreHold = 10 * time.Minute
	// reconnectGrace covers a box agent's reconnect once its socket is back. Its backoff tops out at 10 seconds.
	reconnectGrace = 20 * time.Second

	restoreRetryMin = time.Second
	restoreRetryMax = 30 * time.Second
)

// Restore opens each box's local socket and returns. A deploy can send a host a binary that takes minutes on a relayed link,
// and a host can be down, so each host then comes back by itself in the background, trying until it answers: first the
// forwards that running boxes reconnect through, then this binary, which only a box that starts needs. Nothing a restart
// stops is lost, because the next engine does the same work from the store.
func (b *Boxes) Restore(ctx context.Context) error {
	records, err := b.Store.Boxes(ctx)
	if err != nil {
		return err
	}
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
	}
	hosts, err := b.Store.Hosts(ctx)
	if err != nil {
		return err
	}
	hosts = slices.DeleteFunc(hosts, func(host box.Host) bool { return host.State == box.HostRemoved || host.ExecOnly })
	b.mu.Lock()
	if b.recoveries == nil {
		b.recoveries = make(map[contract.HostName]*recovery)
	}
	for _, host := range hosts {
		b.recoveries[host.Name] = &recovery{done: make(chan struct{}), hold: time.Now().Add(restoreHold)}
	}
	b.mu.Unlock()
	for _, host := range hosts {
		b.recovering.Add(2)
		go func() {
			defer b.recovering.Done()
			b.recoverHost(ctx, host.Name)
		}()
		go func() {
			defer b.recovering.Done()
			b.keepTrying(ctx, host.Name, "identify", func(ctx context.Context) error { return b.identify(ctx, host.Name) })
		}()
	}
	return ctx.Err()
}

// WaitRecovered waits for the recovery work to end, which it does when Restore's context does.
func (b *Boxes) WaitRecovered() { b.recovering.Wait() }

func (b *Boxes) recoverHost(ctx context.Context, name contract.HostName) {
	b.mu.Lock()
	state := b.recoveries[name]
	b.mu.Unlock()
	defer close(state.done)
	steps := box.NewStepLog("host " + string(name))
	restored := b.keepTrying(ctx, name, "restore", func(ctx context.Context) error {
		ctx, cancel := context.WithTimeout(ctx, restoreTry)
		defer cancel()
		return b.restoreHost(ctx, name)
	})
	if !restored {
		return
	}
	steps.Done("restore forwards")
	b.mu.Lock()
	state.hold = time.Now().Add(reconnectGrace)
	b.mu.Unlock()
	if b.keepTrying(ctx, name, "install binary", func(ctx context.Context) error { return b.installHost(ctx, name) }) {
		steps.Done("install binary")
	}
}

// keepTrying runs step until it works or ctx ends, and reports which. It logs an error only when it changes, as a host can
// stay down for days.
func (b *Boxes) keepTrying(ctx context.Context, name contract.HostName, what string, step func(context.Context) error) bool {
	delay := b.retryDelay
	if delay == 0 {
		delay = restoreRetryMin
	}
	last := ""
	for {
		err := step(ctx)
		if err == nil {
			return true
		}
		if ctx.Err() != nil || errors.Is(err, store.ErrNotFound) { // A host removed meanwhile has nothing left to try.
			return false
		}
		if err.Error() != last {
			last = err.Error()
			log.Printf("host %s: %s: %v; retrying", name, what, err)
		}
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return false
		}
		delay = min(delay*2, restoreRetryMax)
	}
}

// restoring reports whether a host's boxes cannot have reconnected yet because the engine has not put their sockets back.
func (b *Boxes) restoring(name contract.HostName) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	state := b.recoveries[name]
	return state != nil && time.Now().Before(state.hold)
}

// recovered reports whether a host's recovery has ended, if it has one.
func (b *Boxes) recovered(name contract.HostName) bool {
	b.mu.Lock()
	state := b.recoveries[name]
	b.mu.Unlock()
	if state == nil {
		return true
	}
	select {
	case <-state.done:
		return true
	default:
		return false
	}
}

// waitRecovered waits for a host's recovery, if it has one, and reports false when ctx ended first.
func (b *Boxes) waitRecovered(ctx context.Context, name contract.HostName) bool {
	b.mu.Lock()
	state := b.recoveries[name]
	b.mu.Unlock()
	if state == nil {
		return true
	}
	select {
	case <-state.done:
		return ctx.Err() == nil
	case <-ctx.Done():
		return false
	}
}

// identify records whether a host is the machine the engine runs on.
func (b *Boxes) identify(ctx context.Context, name contract.HostName) error {
	h, err := b.host(ctx, name)
	if err != nil {
		return err
	}
	same, err := b.engineHost(ctx, h)
	if err != nil {
		return err
	}
	return b.Store.SetEngineHost(ctx, name, same)
}

// restoreHost adds the forward of each box on a host and of its shared mount, which outlives the engine and only waits for its
// socket. A box that fails does not stop the rest. It needs no binary: a running box keeps its agent until it next starts.
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
	records, err := b.Store.Boxes(ctx)
	if err != nil {
		return err
	}
	engineHost, err := b.onEngineHost(ctx, name)
	if err != nil {
		return err
	}
	var failed []error
	if !engineHost {
		if err := host.AddForward(ctx, b.shareForward()); err != nil {
			failed = append(failed, fmt.Errorf("shared folder: %w", err))
		}
	}
	for _, record := range records {
		if record.Host != name || record.State == contract.BoxDeleted || record.State == contract.BoxFailed {
			continue
		}
		if err := host.AddForward(ctx, b.forward(record.ID)); err != nil {
			failed = append(failed, fmt.Errorf("box %s: %w", record.ID, err))
			if ctx.Err() != nil {
				break
			}
		}
	}
	if len(failed) > 0 {
		return errors.Join(failed...)
	}
	b.mu.Lock()
	if b.restored == nil {
		b.restored = make(map[contract.HostName]bool)
	}
	b.restored[name] = true
	b.mu.Unlock()
	return nil
}

// installHost puts this engine's binary on a host once per engine run, for a box about to start.
func (b *Boxes) installHost(ctx context.Context, name contract.HostName) error {
	b.mu.Lock()
	installed := b.installed[name]
	b.mu.Unlock()
	if installed {
		return nil
	}
	host, err := b.host(ctx, name)
	if err != nil {
		return err
	}
	if err := b.installBinary(ctx, host); err != nil {
		return err
	}
	b.mu.Lock()
	if b.installed == nil {
		b.installed = make(map[contract.HostName]bool)
	}
	b.installed[name] = true
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
	ctx, cancel := context.WithTimeout(ctx, box.StartStepBudget)
	defer cancel()
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
	record, err := b.Store.Box(ctx, id)
	if err != nil {
		return err
	}
	if record.State == contract.BoxClosing && record.Host == "" {
		return box.NothingOnHost{Box: string(id)}
	}
	spec := lifecycle.BoxSpec{Box: id, Host: record.Host}
	defer func() { b.mu.Lock(); delete(b.starts, id); b.mu.Unlock() }()
	run := func(step contract.StartStep, work func(context.Context) error) error {
		return b.startStep(ctx, spec, step, work)
	}
	var host *podman.Host
	err = run(contract.StartForward, func(ctx context.Context) error { var e error; host, e = b.containerHost(ctx, id); return e })
	if err != nil {
		return err
	}
	if record.State == contract.BoxClosing {
		if err := closingLeftovers(ctx, host, record); err != nil {
			return err
		}
	}
	if err := run(contract.StartInstructions, func(ctx context.Context) error { return b.syncInstructions(ctx, host) }); err != nil {
		return err
	}
	if err := run(contract.StartBinary, func(ctx context.Context) error { return b.installHost(ctx, record.Host) }); err != nil {
		return err
	}
	session, err := b.Store.Session(ctx, record.Session)
	if err != nil {
		return err
	}
	// A closing box starts only to archive, so neither a host's mount nor a broken sidecar can hold up its close.
	if record.State != contract.BoxClosing {
		if err := run(contract.StartShared, func(ctx context.Context) error { _, e := b.shareReady(ctx, record.Host); return e }); err != nil {
			return err
		}
		if err := run(contract.StartBuildCache, func(ctx context.Context) error { b.startBuildCache(ctx, record.Host, host); return nil }); err != nil {
			return err
		}
		if session.Compose != "" {
			if err := run(contract.StartSidecars, func(ctx context.Context) error { return host.StartSidecars(ctx, id) }); err != nil {
				return err
			}
		}
	}
	return run(contract.StartContainer, func(ctx context.Context) error { return host.StartContainer(ctx, id) })
}

// closingLeftovers refuses to start a closing box that has neither container nor volume on its host, as when its create never
// worked there. Its work would be in the volume, so there is nothing to archive, and a start could only fail every time.
func closingLeftovers(ctx context.Context, host interface {
	HoldsBox(context.Context, contract.BoxID) (bool, error)
}, record box.Box) error {
	holds, err := host.HoldsBox(ctx, record.ID)
	if err != nil || holds {
		return err
	}
	return box.NothingOnHost{Box: string(record.ID), Host: string(record.Host)}
}

// Stop stops a box and, once it is down, the rest of its pod.
func (b *Boxes) Stop(ctx context.Context, id contract.BoxID) error {
	ctx = podman.WithRemoteCancellation(ctx)
	host, err := b.containerHost(ctx, id)
	if err != nil {
		return err
	}
	if err := host.StopContainer(ctx, id); err != nil {
		return err
	}
	return host.StopPod(ctx, id)
}

func (b *Boxes) Remove(ctx context.Context, id contract.BoxID) error {
	ctx = podman.WithRemoteCancellation(ctx)
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
	if err := h.RemovePod(ctx, record.ID); err != nil {
		return err
	}
	if err := h.RemoveVolume(ctx, record.ID); err != nil {
		return err
	}
	if err := h.RemoveStackVolumes(ctx, record.ID); err != nil {
		return err
	}
	// A box closed before it was created never got a slice, so there is none to remove.
	if record.Slice == "" {
		return nil
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

func (b *Boxes) ExecArgv(ctx context.Context, host contract.HostName, id contract.BoxID, argv ...string) ([]string, error) {
	h, err := b.host(ctx, host)
	if err != nil {
		return nil, err
	}
	return h.ExecArgv(id, argv...), nil
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
	ctx, cancel := context.WithTimeout(ctx, box.StartPreparationBudget)
	defer cancel()
	h, err := b.host(ctx, host)
	if err != nil {
		return box.Image{}, err
	}
	session, err := b.Store.Session(ctx, sessionID)
	if err != nil {
		return box.Image{}, err
	}
	var size contract.BoxSize
	if session.Compose != "" {
		record, err := b.Store.Box(ctx, session.Box)
		if err != nil {
			return box.Image{}, err
		}
		size = record.Size
	}
	image, err := b.image(ctx, h, host, session.Room, session.Repo, session.Compose, size)
	pinned, pinErr := b.Store.PinWorkflowImage(ctx, sessionID, image.Tag)
	if pinErr != nil {
		return box.Image{}, pinErr
	}
	// A crew keeps the image it started on, even when the repository's own no longer builds. A host without that tag can only
	// build what the repository holds now.
	if pinned != "" && pinned != image.Tag {
		if has, hasErr := h.HasImage(ctx, pinned); hasErr == nil && has {
			image.Tag = pinned
			return image, nil
		}
	}
	return image, err
}

// image selects and builds what a box uses. Every box takes its image from here, whether new, swapped or built ahead. A
// compose name brings its file, which chooses the image and adds the sidecars; size checks the file's limits, and the zero
// size of a build ahead skips that.
func (b *Boxes) image(ctx context.Context, h *podman.Host, host contract.HostName, room contract.RoomID, repo contract.RepoName, composeName string, size contract.BoxSize) (box.Image, error) {
	ctx, cancel := context.WithTimeout(ctx, box.StartPreparationBudget)
	defer cancel()
	steps := box.NewStepLog("host " + string(host))
	base, err := h.BuildBase(ctx, images.BaseContainerfile)
	if err != nil {
		return box.Image{}, err
	}
	steps.Done("base image")
	// A room's repository images move into the room before its image is chosen, so a late mirror loses nothing.
	if room != "" && b.Migrator != nil {
		b.Migrator.Room(ctx, room, true)
	}
	var file *compose.File
	folder := ""
	if composeName != "" {
		if file, err = b.Compose.Load(ctx, room, repo, composeName); err != nil {
			return box.Image{}, err
		}
		if size.Name != "" {
			if err := file.CheckLimits(size); err != nil {
				return box.Image{}, err
			}
		}
		folder = file.Agent.Build
		steps.Done("compose file")
	}
	tag, needs, err := b.toolchain(ctx, h, host, room, folder, base)
	if err != nil {
		return box.Image{}, err
	}
	image := box.Image{Tag: tag, Needs: needs}
	if file != nil {
		if image.Stack, err = b.stack(ctx, h, host, room, repo, base, file); err != nil {
			return box.Image{}, err
		}
		steps.Done("sidecar images")
	}
	return image, nil
}

// toolchain builds one image of the room: the one called name, or its sole box image when name is empty. With neither it is base.
func (b *Boxes) toolchain(ctx context.Context, h *podman.Host, host contract.HostName, room contract.RoomID, name, base string) (string, []box.Need, error) {
	if room == "" {
		return base, nil, nil
	}
	steps := box.NewStepLog("host " + string(host))
	name, tree, data, needs, err := b.RoomImages.Context(room, name)
	if err != nil {
		return "", nil, err
	}
	steps.Done("image context")
	if name == "" {
		return base, needs, nil
	}
	tag, err := b.Images.Build(ctx, h, room, host, "", name, base, tree, data)
	if err != nil {
		return "", nil, err
	}
	steps.Done("toolchain image")
	return tag, needs, nil
}

// stack gets each sidecar's image onto the host. box-needs grants devices, which a sidecar never gets.
func (b *Boxes) stack(ctx context.Context, h *podman.Host, host contract.HostName, room contract.RoomID, repo contract.RepoName, base string, file *compose.File) (*box.Stack, error) {
	tags := make(map[string]string, len(file.Sidecars))
	for _, sidecar := range file.Sidecars {
		if sidecar.Image != "" {
			if err := h.PullImage(ctx, sidecar.Image); err != nil {
				return nil, err
			}
			tags[sidecar.Name] = sidecar.Image
			continue
		}
		tag, needs, err := b.toolchain(ctx, h, host, room, sidecar.Build, base)
		if err != nil {
			return nil, err
		}
		if len(needs) > 0 {
			return nil, box.StackError{Reason: fmt.Sprintf("service %s: image %s has box-needs, which is only for the agent", sidecar.Name, sidecar.Build)}
		}
		tags[sidecar.Name] = tag
	}
	return file.Stack(base, tags), nil
}

func (b *Boxes) StopHostexec(ctx context.Context, host contract.HostName, scope string) error {
	r, err := b.scopeRunner(ctx, host)
	if err != nil {
		return err
	}
	return r.StopHostexec(ctx, scope)
}

// Recreate replaces a container without touching its volume.
func (b *Boxes) Recreate(ctx context.Context, spec lifecycle.BoxSpec, image string) error {
	defer func() { b.mu.Lock(); delete(b.starts, spec.Box); b.mu.Unlock() }()
	run := func(step contract.StartStep, work func(context.Context) error) error {
		return b.startStep(ctx, spec, step, work)
	}
	steps := box.NewStepLog("box " + string(spec.Box))
	if err := run(contract.StartForward, func(ctx context.Context) error { return b.restoreHost(ctx, spec.Host) }); err != nil {
		return err
	}
	if err := run(contract.StartBinary, func(ctx context.Context) error { return b.installHost(ctx, spec.Host) }); err != nil {
		return err
	}
	h, err := b.host(ctx, spec.Host)
	if err != nil {
		return err
	}
	if err := run(contract.StartInstructions, func(ctx context.Context) error { return b.syncInstructions(ctx, h) }); err != nil {
		return err
	}
	steps.Done("instructions")
	ok := false
	defer func() {
		if !ok {
			cleanup, stop := startCleanupContext(ctx)
			defer stop()
			_ = h.StopContainer(cleanup, spec.Box)
			_ = h.StopPod(cleanup, spec.Box)
		}
	}()
	if err := run(contract.StartBuildCache, func(ctx context.Context) error { b.startBuildCache(ctx, spec.Host, h); return nil }); err != nil {
		return err
	}
	steps.Done("build cache")
	var shared box.Mount
	err = run(contract.StartShared, func(ctx context.Context) error {
		var e error
		shared, e = b.sharedMount(ctx, spec.Host, spec.Room)
		return e
	})
	if err != nil {
		return err
	}
	steps.Done("shared folder")
	session, err := b.Store.Session(ctx, spec.Session)
	if err != nil {
		return err
	}
	spec.Root = session.Root
	if err := run(contract.StartCreate, func(ctx context.Context) error { return h.StopContainer(ctx, spec.Box) }); err != nil {
		return err
	}
	if err := run(contract.StartCreate, func(ctx context.Context) error { return h.RemoveContainer(ctx, spec.Box) }); err != nil {
		return err
	}
	if spec.Stack != nil {
		// The pod goes whole: its sidecars follow the box to new images, and its volumes stay.
		if err := run(contract.StartStack, func(ctx context.Context) error { return h.RemovePod(ctx, spec.Box) }); err != nil {
			return err
		}
	}
	steps.Done("remove old container")
	slice, c := b.container(spec, image, shared)
	if err := run(contract.StartSlice, func(ctx context.Context) error { return h.EnsureSlice(ctx, slice) }); err != nil {
		return err
	}
	steps.Done("slice")
	if spec.Stack != nil {
		if err := run(contract.StartStack, func(ctx context.Context) error { return h.CreateStack(ctx, spec.Box, spec.Slice, spec.Stack) }); err != nil {
			return err
		}
		steps.Done("compose stack")
	}
	if err := run(contract.StartCreate, func(ctx context.Context) error { return h.CreateContainer(ctx, c) }); err != nil {
		var missing box.MissingDevice
		if errors.As(err, &missing) {
			missing.Host = string(spec.Host)
			return missing
		}
		return err
	}
	steps.Done("container create")
	if spec.Stack != nil {
		if err := run(contract.StartSidecars, func(ctx context.Context) error { return h.StartSidecars(ctx, spec.Box) }); err != nil {
			return err
		}
		steps.Done("sidecars")
	}
	if err := run(contract.StartContainer, func(ctx context.Context) error { return h.StartContainer(ctx, spec.Box) }); err != nil {
		return err
	}
	steps.Done("container start")
	ok = true
	return nil
}

func (b *Boxes) container(spec lifecycle.BoxSpec, image string, shared box.Mount) (box.Slice, box.ContainerSpec) {
	size := spec.Size
	slice := box.Slice{Name: spec.Slice, CPUs: size.CPUs, MemHigh: size.MemBytes, MemMax: size.MemBytes * 3 / 2, Pids: size.Pids}
	c := box.ContainerSpec{Box: spec.Box, Image: image, Slice: spec.Slice, Needs: spec.Needs, GPUCard: spec.GPUCard, Labels: []box.Label{{Key: "dyna.layout", Value: box.ContainerLayout}}, Mounts: []box.Mount{
		{HostPath: b.Layout.HostSocketFolder(spec.Box), BoxPath: "/run/dyna/engine", ReadOnly: true},
		{HostPath: b.Layout.HostBin(), BoxPath: "/opt/dyna/bin", ReadOnly: true},
		{HostPath: b.Layout.HostCache(), BoxPath: "/box/cache"},
		{HostPath: b.Layout.HostBuildCacheSocketFolder(), BoxPath: "/run/dyna/build-cache", ReadOnly: true, MakeDir: true},
		shared,
		{HostPath: filepath.Dir(b.Layout.HostInstructions("")), BoxPath: "/run/dyna/instructions/global", ReadOnly: true, MakeDir: true},
		{HostPath: filepath.Dir(b.Layout.HostInstructions(spec.Room)), BoxPath: "/run/dyna/instructions/room", ReadOnly: true, MakeDir: true},
	}, Env: []box.EnvVar{{Name: "GOMODCACHE", Value: "/box/cache/gomod"}, {Name: "GOCACHE", Value: "/box/cache/gocache"}, {Name: "CARGO_HOME", Value: box.CargoHome}, {Name: "npm_config_store_dir", Value: "/box/cache/pnpm"}}}
	if spec.Root {
		c.Mounts = append(c.Mounts,
			box.Mount{HostPath: filepath.Dir(b.Layout.HostRootAddons("")), BoxPath: "/run/dyna/root-addons/global", ReadOnly: true, MakeDir: true},
			box.Mount{HostPath: filepath.Dir(b.Layout.HostRootAddons(spec.Room)), BoxPath: "/run/dyna/root-addons/room", ReadOnly: true, MakeDir: true})
	}
	for _, repo := range spec.Repos {
		c.Mounts = append(c.Mounts, box.Mount{HostPath: repo.Cache, BoxPath: repo.Cache, ReadOnly: true})
	}
	// Cargo's default for the checkout, set because a CI runner of the repository sets it, and sccache hashes every CARGO_
	// variable: with the same two, each hits what the other compiled.
	if len(spec.Repos) > 0 {
		if work, err := box.RepoFolder(spec.Repos[0].Name); err == nil {
			c.Env = append(c.Env, box.EnvVar{Name: "CARGO_TARGET_DIR", Value: filepath.Join(work, "target")})
		}
	}
	if stack := spec.Stack; stack != nil {
		c.Env = append(c.Env, stack.Env...)
		c.Volumes = stack.Volumes
		c.Labels = append(c.Labels, box.Label{Key: podman.StackHashLabel, Value: stack.Hash})
		if len(stack.Sidecars) > 0 {
			c.Pod = box.PodName(spec.Box)
		}
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

// PromoteHost runs the full host check on an exec-only host that is to take boxes or runners, installs what it needs and
// records what the check found. An error leaves the host exec only.
func (b *Boxes) PromoteHost(ctx context.Context, name contract.HostName, ownerOnly bool) error {
	hosts, err := b.Store.Hosts(ctx)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(hosts, func(h box.Host) bool { return h.Name == name })
	if i < 0 {
		return fmt.Errorf("host %q: %w", name, store.ErrNotFound)
	}
	h, err := b.host(ctx, name)
	if err != nil {
		return err
	}
	record, _, err := b.setUpHost(ctx, h, name, hosts[i].SSH, ownerOnly)
	if err != nil {
		return err
	}
	return b.Store.SetHostChecked(ctx, record)
}
