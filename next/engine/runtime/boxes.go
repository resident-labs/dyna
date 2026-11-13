// Package runtime connects the engine to host transports.
package runtime

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/box/podman"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine"
	"github.com/resident-labs/dyna/engine/addons"
	"github.com/resident-labs/dyna/engine/api"
	"github.com/resident-labs/dyna/engine/compose"
	enginegit "github.com/resident-labs/dyna/engine/git"
	engineimages "github.com/resident-labs/dyna/engine/images"
	"github.com/resident-labs/dyna/engine/lifecycle"
	"github.com/resident-labs/dyna/engine/store"
)

type boxStore interface {
	Done() <-chan struct{}
	Hosts(context.Context) ([]box.Host, error)
	AddHost(context.Context, box.Host) error
	SetHostChecked(context.Context, box.Host) error
	SetRoomShared(context.Context, []contract.RoomShared, time.Time) error
	SetEngineHost(context.Context, contract.HostName, bool) error
	Session(context.Context, contract.SessionID) (store.Session, error)
	Box(context.Context, contract.BoxID) (box.Box, error)
	CanRemoveBoxVolumes(context.Context, contract.BoxID) (bool, error)
	RemoveBoxVolumes(context.Context, contract.BoxID, contract.HostName, func(context.Context) error) (bool, error)
	Boxes(context.Context) ([]box.Box, error)
	Services(context.Context, contract.RoomID) ([]store.ServiceRow, error)
	OpenRooms(context.Context) ([]contract.RoomID, error)
	RoomRepos(context.Context, contract.RoomID) ([]contract.RoomRepo, error)
	ActiveRuns(context.Context) ([]store.HookRun, error)
	PinWorkflowImage(context.Context, contract.SessionID, string) (string, error)
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
	installed         map[contract.HostName]bool   // immutable binary staged in this process; current can still drift
	shareProblems     map[contract.HostName]string // the last reason each host's mount could not come up
	recoveries        map[contract.HostName]*recovery
	recovering        sync.WaitGroup
	retryDelay        time.Duration // retryDelay is the first wait between recovery tries; zero means restoreRetryMin.
	loads             map[contract.HostName]hostLoad
	beats             map[contract.HostName]context.CancelFunc // each host's heartbeat
	heartbeatTicks    <-chan time.Time                         // nil uses the normal ticker; tests can drive one complete survey.
	cpuPrev           *cpuTimes                                // cpuPrev is the last /proc/stat sample of this machine, nil before the first.
	installs          map[*podman.Host]*binaryInstall
	binaryRepairs     map[*podman.Host]bool
	starts            map[contract.BoxID]contract.BoxStartStatus
	stepContext       func(context.Context, time.Duration) (context.Context, context.CancelFunc)
	binCacheOnce      sync.Once
	binCache          *podman.BinaryCache
	binOnce           sync.Once
	binSum            string
	binErr            error
}

// startBuildCache starts the host's compiler cache for a box or runner about to start there. A build without it compiles every
// crate itself, so a failure is only logged.
func (b *Boxes) startBuildCache(ctx context.Context, name contract.HostName, h *podman.Host) {
	if err := h.EnsureBuildCache(ctx, b.Layout); err != nil && ctx.Err() == nil {
		log.Printf("host %s: build cache: %v", name, err)
	}
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
	ticks := ticker.C
	if b.heartbeatTicks != nil {
		ticker.Stop()
		ticks = b.heartbeatTicks
	}
	go func() {
		defer ticker.Stop()
		h.Heartbeat(ctx, ticks, b.Layout, func(ok bool, load podman.Load) {
			now := time.Now()
			if ok && load.Probe == podman.ProbeGood && !load.ExecOnly {
				b.repairBinary(ctx, h, load.ActiveBinary)
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
			h, err := podman.New(record.SSH, b.Runtime, string(name), nil, b.Layout.HostData)
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

// RemoveLeftoverBox cleans runnable resources without treating ordinary failure
// debt as permission to erase named work. Incomplete debt remains discoverable by
// an explicit owner restart, close/discard or room deletion.
func (b *Boxes) RemoveLeftoverBox(ctx context.Context, name contract.HostName, id contract.BoxID, slice string) (lifecycle.LeftoverCleanup, error) {
	record, err := b.Store.Box(ctx, id)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return lifecycle.LeftoverDeferred, err
	}
	// These names now belong to an active current incarnation. Do not touch it,
	// and do not claim that its retained volumes were deleted.
	if err == nil && record.Host == name && record.State != contract.BoxFailed && record.State != contract.BoxDeleted {
		return lifecycle.LeftoverDeferred, nil
	}
	h, err := b.host(ctx, name)
	if err != nil {
		return lifecycle.LeftoverDeferred, err
	}
	ctx = podman.WithRemoteCancellation(ctx)
	removed, err := b.removeBoxVolumes(ctx, name, h, box.Box{ID: id, Slice: slice})
	var superseded box.NamespaceSuperseded
	if errors.As(err, &superseded) {
		return lifecycle.LeftoverComplete, nil
	}
	if err != nil {
		return lifecycle.LeftoverDeferred, err
	}
	if removed {
		return lifecycle.LeftoverComplete, nil
	}
	return lifecycle.LeftoverContainersRemoved, nil
}

// ForgetBox closes the engine's socket for a box that its offline host still holds.
func (b *Boxes) ForgetBox(id contract.BoxID) error { return b.Sockets.RemoveBox(id) }

func (b *Boxes) CacheHost(ctx context.Context, name contract.HostName) (enginegit.CacheHost, error) {
	return b.host(ctx, name)
}

func (b *Boxes) AddHost(ctx context.Context, req contract.HostAddRequest) (bool, error) {
	h, err := podman.New(req.SSH, b.Runtime, string(req.Name), b.hostRunner, b.Layout.HostData)
	if err != nil {
		return false, err
	}
	// An exec-only host passes the base host checks. Nothing is installed there and no box or runner is ever placed there.
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
	h, err := podman.New(ssh, b.Runtime, string(name), b.hostRunner, b.Layout.HostData)
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
	return box.Host{EngineHost: &same, Name: name, SSH: ssh, State: box.HostReady, Problem: problem, CIScale: 1, CPUs: check.CPUs, MemBytes: check.MemBytes, DiskBytes: check.DiskBytes, CheckedAt: time.Now()}, check.NoSwap, nil
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
	steps := box.NewStepLog("box " + string(spec.Box))
	run := func(step contract.StartStep, label string, work func(context.Context) error) error {
		if err := b.startStep(ctx, spec, step, work); err != nil {
			return err
		}
		steps.Done(label)
		return nil
	}
	h, err := b.host(ctx, spec.Host)
	if err != nil {
		return "", err
	}
	if err := run(contract.StartInstructions, "instructions", func(ctx context.Context) error { return b.syncInstructions(ctx, h) }); err != nil {
		return "", err
	}
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
	if err = run(contract.StartBinary, "binary", func(ctx context.Context) error { return b.installBinary(ctx, h) }); err != nil {
		return "", err
	}
	if err = run(contract.StartBuildCache, "build cache", func(ctx context.Context) error { b.startBuildCache(ctx, spec.Host, h); return nil }); err != nil {
		return "", err
	}
	var shared box.Mount
	err = run(contract.StartShared, "shared folder", func(ctx context.Context) error {
		var e error
		shared, e = b.sharedMount(ctx, spec.Host, spec.Room)
		return e
	})
	if err != nil {
		return "", err
	}
	var image box.Image
	err = run(contract.StartImage, "image", func(ctx context.Context) error {
		var e error
		image, e = b.CurrentImage(ctx, spec.Session, spec.Host)
		return e
	})
	if err != nil {
		return "", err
	}
	spec.Needs, spec.Stack = image.Needs, image.Stack
	slice, c := b.container(spec, image.Tag, shared)
	// Mark attempts before the step: a failed or timed-out host call may
	// already have created resources and must still be cleaned up.
	sliced = true
	if err = run(contract.StartSlice, "slice", func(ctx context.Context) error { return h.EnsureSlice(ctx, slice) }); err != nil {
		return "", box.RuntimeCreateError{Err: err}
	}
	forwarded = true
	if err = run(contract.StartForward, "forward", func(ctx context.Context) error { return h.AddForward(ctx, forward) }); err != nil {
		return "", box.RuntimeCreateError{Err: err}
	}
	stacked = spec.Stack != nil
	created = true
	if err = run(contract.StartCreate, "container create", func(ctx context.Context) error { return h.CreateBox(ctx, c, spec.Stack, false) }); err != nil {
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
	if spec.Stack != nil {
		if err = run(contract.StartSidecars, "sidecars", func(ctx context.Context) error { return h.StartSidecars(ctx, spec.Box) }); err != nil {
			return "", err
		}
	}
	if err = run(contract.StartContainer, "container start", func(ctx context.Context) error { return h.StartContainer(ctx, spec.Box) }); err != nil {
		return "", box.RuntimeCreateError{Err: err}
	}
	ok = true
	return image.Tag, nil
}

// cleanupCreate removes what a failed creation made, on its own bounded context: the creation context is often the reason it failed.
func cleanupCreate(ctx context.Context, h box.HostDriver, spec lifecycle.BoxSpec, _ box.Forward, stacked, created, forwarded, sliced bool) {
	ctx, cancel := startCleanupContext(ctx)
	defer cancel()
	if stacked || created || sliced || forwarded {
		epoch, err := h.BoxEpoch(ctx, spec.Box)
		if err == nil {
			slice := ""
			if sliced {
				slice = spec.Slice
			}
			err = h.RemoveBox(ctx, spec.Box, epoch, false, slice)
			var superseded box.NamespaceSuperseded
			if errors.As(err, &superseded) {
				return
			}
		}
	}
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
	status, found, err := inspectContainer(ctx, host, id)
	if err != nil || !found {
		return status, found, err
	}
	wanted, err := b.binarySum()
	if err != nil {
		return box.ContainerStatus{}, false, err
	}
	status.StaleLayout = status.StaleLayout || status.Binary != wanted
	return status, true, nil
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
	epoch, err := host.BoxEpoch(ctx, id)
	if err != nil {
		return err
	}
	err = host.StopBox(ctx, id, epoch)
	var superseded box.NamespaceSuperseded
	if errors.As(err, &superseded) {
		return nil
	}
	return err
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
	if _, err := b.removeBoxVolumes(ctx, record.Host, h, record); err != nil {
		var superseded box.NamespaceSuperseded
		if errors.As(err, &superseded) {
			return nil
		}
		return err
	}
	return b.Sockets.RemoveBox(id)
}

// RemoveFailedBox is the synchronous destructive half of authorized SessionRestart.
// Its caller owns the process-local fence and checked the failed snapshot. Never
// resolve the row again here: a quarantined old host must be cleaned before detach.
func (b *Boxes) RemoveFailedBox(ctx context.Context, old box.Box) error {
	if old.Host == "" {
		return nil // never assigned, or explicitly detached by host removal
	}
	hosts, err := b.Store.Hosts(ctx)
	if err != nil {
		return err
	}
	if !slices.ContainsFunc(hosts, func(h box.Host) bool { return h.Name == old.Host && h.State == box.HostReady }) {
		return fmt.Errorf("old host %s is unavailable; failed box was not reset", old.Host)
	}
	return b.DiscardHostBox(ctx, old)
}

// DiscardHostBox removes captured names under explicit destructive owner authority:
// host discard has closed admission and failed its boxes; SessionRestart wraps this
// operation with its failed-snapshot fence and available-host check. Never use it
// for ordinary Stop/recovery. The host-discard caller keeps its bounded best-effort
// semantics and may abandon the host after an error; restart instead retains debt.
func (b *Boxes) DiscardHostBox(ctx context.Context, old box.Box) error {
	generation := b.Sockets.BoxGeneration(old.ID)
	ctx = podman.WithRemoteCancellation(ctx)
	h, err := b.host(ctx, old.Host)
	if err != nil {
		return err
	}
	if err := removeHostBox(ctx, h, old, b.forward(old.ID), true); err != nil {
		var superseded box.NamespaceSuperseded
		if errors.As(err, &superseded) {
			return nil
		}
		return err
	}
	return b.Sockets.RemoveBoxAtGeneration(old.ID, generation)
}

// removeHostBox removes runnable resources. Named work requires explicit deletion permission from its owner.
func removeHostBox(ctx context.Context, h box.HostDriver, record box.Box, forward box.Forward, removeVolumes bool) error {
	epoch, err := h.BoxEpoch(ctx, record.ID)
	if err != nil {
		return err
	}
	return removeHostBoxAtEpoch(ctx, h, record, forward, epoch, removeVolumes)
}
func removeHostBoxAtEpoch(ctx context.Context, h box.HostDriver, record box.Box, _ box.Forward, epoch uint64, removeVolumes bool) error {
	return h.RemoveBox(ctx, record.ID, epoch, removeVolumes, record.Slice)
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
			if epoch, err := h.BoxEpoch(cleanup, spec.Box); err == nil {
				_ = h.StopBox(cleanup, spec.Box, epoch)
			}
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
	slice, c := b.container(spec, image, shared)
	if err := run(contract.StartSlice, func(ctx context.Context) error { return h.EnsureSlice(ctx, slice) }); err != nil {
		return err
	}
	steps.Done("slice")
	if err := run(contract.StartCreate, func(ctx context.Context) error { return h.CreateBox(ctx, c, spec.Stack, true) }); err != nil {
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
	c := box.ContainerSpec{Box: spec.Box, Image: image, Slice: spec.Slice, CPUs: slice.CPUs, MemBytes: slice.MemMax, Needs: spec.Needs, GPUCard: spec.GPUCard, Labels: []box.Label{{Key: "dyna.layout", Value: box.ContainerLayout}, {Key: "dyna.binary", Value: b.binSum}}, Mounts: []box.Mount{
		{HostPath: b.Layout.HostSocketFolder(spec.Box), BoxPath: "/run/dyna/engine", ReadOnly: true},
		{HostPath: filepath.Join(b.Layout.HostBin(), b.binSum), BoxPath: "/opt/dyna/bin/current", ReadOnly: true},
		{HostPath: b.Layout.HostCache(), BoxPath: "/box/cache"},
		{HostPath: b.Layout.HostBuildCacheSocketFolder(), BoxPath: "/run/dyna/build-cache", ReadOnly: true, MakeDir: true},
		shared,
		{HostPath: filepath.Dir(b.Layout.HostInstructions("")), BoxPath: "/run/dyna/instructions/global", ReadOnly: true, MakeDir: true},
		{HostPath: filepath.Dir(b.Layout.HostInstructions(spec.Room)), BoxPath: "/run/dyna/instructions/room", ReadOnly: true, MakeDir: true},
	}, Env: []box.EnvVar{{Name: "GOMODCACHE", Value: "/box/cache/gomod"}, {Name: "GOCACHE", Value: "/box/cache/gocache"}, {Name: "CARGO_HOME", Value: box.CargoHome}, {Name: "npm_config_store_dir", Value: "/box/cache/pnpm"}}}
	// nproc reads this, and not cpu.max, so make -j$(nproc) matches the cap.
	if slice.CPUs > 0 {
		c.Env = append(c.Env, box.EnvVar{Name: "OMP_THREAD_LIMIT", Value: strconv.Itoa(slice.CPUs)})
	}
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
