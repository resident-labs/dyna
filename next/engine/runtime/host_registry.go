package runtime

import (
	"context"
	"fmt"
	"log"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/box/podman"
	"github.com/resident-labs/dyna/contract"
	enginegit "github.com/resident-labs/dyna/engine/git"
	"github.com/resident-labs/dyna/engine/lifecycle"
	"github.com/resident-labs/dyna/engine/store"
)

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
