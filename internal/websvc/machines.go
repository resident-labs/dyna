package websvc

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"slices"
	"strings"
	"time"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/jobs"
	"dyna-agent/internal/machines"
)

// MachineLive is what this engine's own machine uses now. The engine fills
// only what it can read, so a figure without a sample stays nil.
type MachineLive struct {
	CPU           *int
	MemoryPercent *int
	Agents        *int
	Computers     *int
	Harnesses     []MachineHarness
}

// MachineAPI answers the machine calls: the list Home shows and one machine's
// page. Only this engine's own machine reports live figures; a remote machine
// answers with what the registry stores until the probe reads it.
type MachineAPI struct {
	Store machines.Store
	// Version is this build of Dyna, shown for this engine's machine.
	Version string
	// Live reads this engine's machine. It runs for the local row only.
	Live func(ctx context.Context) MachineLive
	// Running lists the sessions that run on this engine's machine now. Only
	// the page reads it: it costs one session listing.
	Running func() []MachineAgent
	// CI lists the local CI runs in flight on every machine, which the engine
	// takes from its scheduler. nil means the engine runs no local CI, so a CI
	// machine keeps showing its slots and no use.
	CI func() []MachineCIRun
	// CIQueue reads a node's shared queue: how much work it runs now and how
	// much waits for room. Nil leaves the running count to the CI runs.
	CIQueue func(node string) (running, waiting int)
	// Work reads a machine's queue and running work. Nil means the engine runs
	// no jobs, so the machine has no work to show.
	Work func(ctx context.Context, m machines.Machine) (jobs.NodeWork, error)
	// SessionName names a session from its ID. A nil SessionName leaves the
	// name out.
	SessionName func(sessionID string) string
	// Prober reads a machine when the owner asks for a probe. A nil Prober
	// reads it through this registry with no load sampler of this engine.
	Prober      *machines.Prober
	RecordProbe func(machines.Machine, machines.ProbeResult) error
	// RoomName names a room from its ID, so the page says which room allows
	// host checks and not only its ID. A nil RoomName shows the ID.
	RoomName func(roomID string) string
	// Alerter posts the low-disk alert of a machine whose disk starts to block
	// new work. A nil Alerter posts nothing.
	Alerter machines.Alerter
}

// Nodes lists every node, the engine host first.
func (a MachineAPI) Nodes(ctx context.Context) ([]MachineSummary, error) {
	list, err := a.Store.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]MachineSummary, 0, len(list))
	runs := a.ciRuns()
	for _, m := range list {
		out = append(out, a.machine(ctx, m, runs).MachineSummary)
	}
	return out, nil
}

// Node reads one node's page by its ID.
func (a MachineAPI) Node(ctx context.Context, id string) (MachineDetail, error) {
	m, err := a.find(ctx, id)
	if err != nil {
		return MachineDetail{}, err
	}
	return a.machine(ctx, m, a.ciRuns()), nil
}

// NodeWork reads the Work section of one node's page by its ID.
func (a MachineAPI) NodeWork(ctx context.Context, id string) (MachineWork, error) {
	m, err := a.find(ctx, id)
	if err != nil {
		return MachineWork{}, err
	}
	var node jobs.NodeWork
	if a.Work != nil {
		if node, err = a.Work(ctx, m); err != nil {
			return MachineWork{}, err
		}
	}
	named := func(items []jobs.WorkItem) []MachineWorkItem {
		out := make([]MachineWorkItem, 0, len(items))
		for _, item := range items {
			named := MachineWorkItem{WorkItem: item}
			if a.RoomName != nil && item.Room != "" {
				named.RoomName = a.RoomName(item.Room)
			}
			if a.SessionName != nil && item.Session != "" {
				named.SessionName = a.SessionName(item.Session)
			}
			out = append(out, named)
		}
		return out
	}
	return MachineWork{Capacity: node.Capacity, Running: named(node.Running), Waiting: named(node.Waiting)}, nil
}

// SaveNode applies one change to the machine that ref names, by ID or by
// name, and answers with the page the machine has after it. It is the one
// machine mutation path for the web and the CLI: it normalizes the result and
// the store validates it before it writes.
func (a MachineAPI) SaveNode(ctx context.Context, ref string, patch MachinePatch) (MachineDetail, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return MachineDetail{}, errors.New("a node reference is required")
	}
	m, found, err := a.resolve(ctx, ref)
	if err != nil {
		return MachineDetail{}, err
	}
	if patch.Remove {
		if !found {
			return MachineDetail{}, fmt.Errorf("node %q: %w", ref, os.ErrNotExist)
		}
		removed := a.machine(ctx, m, a.ciRuns())
		if err := a.Store.Remove(ctx, m.Name); err != nil {
			return MachineDetail{}, err
		}
		return removed, nil
	}
	if found && patch.Create {
		return MachineDetail{}, fmt.Errorf("node %s exists; use dyna node edit", m.Name)
	}
	apply := func(stored *machines.Machine) error {
		if err := applyMachinePatch(stored, patch); err != nil {
			return err
		}
		if patch.AllowHostChecks != nil {
			grants, err := hostChecksGrants(stored.HostChecksGrants, authority.Of(ctx), *patch.AllowHostChecks)
			if err != nil {
				return err
			}
			stored.HostChecksGrants = grants
		}
		return nil
	}
	if found {
		// The complete patch reads, validates and writes one fresh row. A failed
		// registration cannot commit another field, and a stale read cannot
		// re-enable a node or restore revoked access.
		saved, err := a.Store.Update(ctx, m.ID, apply)
		if err != nil {
			return MachineDetail{}, err
		}
		return a.machine(ctx, saved, a.ciRuns()), nil
	}
	if !patch.Create {
		return MachineDetail{}, fmt.Errorf("node %q: %w", ref, os.ErrNotExist)
	}
	m = machines.Machine{Name: ref, Enabled: true}
	if err := apply(&m); err != nil {
		return MachineDetail{}, err
	}
	saved, err := a.Store.Save(ctx, m)
	if err != nil {
		return MachineDetail{}, err
	}
	return a.machine(ctx, saved, a.ciRuns()), nil
}

// applyMachinePatch writes the fields the patch names.
func applyMachinePatch(m *machines.Machine, patch MachinePatch) error {
	if patch.Name != nil {
		name := strings.TrimSpace(*patch.Name)
		if err := machines.ValidateName(name); err != nil {
			return err
		}
		m.Name = name
	}
	if patch.Enabled != nil {
		m.Enabled = *patch.Enabled
	}
	if patch.Rooms != nil {
		m.Rooms = *patch.Rooms
	}
	if patch.Labels != nil {
		m.Labels = *patch.Labels
	}
	if patch.SSH != nil {
		m.SSH = strings.TrimSpace(*patch.SSH)
	}
	if patch.ContainerSocket != nil {
		m.ContainerSocket = strings.TrimSpace(*patch.ContainerSocket)
	}
	if patch.CheckTmp != nil {
		m.CheckTmp = strings.TrimSpace(*patch.CheckTmp)
	}
	if patch.SSHConnectTimeoutSeconds != nil {
		m.SSHConnectTimeout = time.Duration(*patch.SSHConnectTimeoutSeconds) * time.Second
	}
	if patch.MaxIOHeavy != nil {
		m.MaxIOHeavy = *patch.MaxIOHeavy
	}
	if patch.MaxDiskPercent != nil {
		m.MaxDiskPercent = *patch.MaxDiskPercent
	}
	if patch.Resources != nil {
		m.Resources = *patch.Resources
	}
	if patch.Mounts != nil {
		for _, update := range *patch.Mounts {
			at := slices.IndexFunc(m.Mounts, func(stored machines.NamedMount) bool {
				return stored.Name == update.Name && stored.Room == update.Room
			})
			if at >= 0 {
				m.Mounts[at] = update
			} else {
				m.Mounts = append(m.Mounts, update)
			}
		}
	}
	if patch.Devices != nil {
		for _, update := range *patch.Devices {
			at := slices.IndexFunc(m.Devices, func(stored machines.DeviceGrant) bool {
				return stored.Path == update.Path && stored.Room == update.Room
			})
			if at >= 0 {
				m.Devices[at] = update
			} else {
				m.Devices = append(m.Devices, update)
			}
		}
	}
	if patch.RemoveMounts != nil {
		for _, remove := range *patch.RemoveMounts {
			if err := remove.Validate(); err != nil {
				return err
			}
			m.Mounts = slices.DeleteFunc(m.Mounts, func(stored machines.NamedMount) bool {
				return stored.Name == remove.Name && stored.Room == remove.Room
			})
		}
	}
	if patch.RemoveDevices != nil {
		for _, remove := range *patch.RemoveDevices {
			if err := remove.Validate(); err != nil {
				return err
			}
			m.Devices = slices.DeleteFunc(m.Devices, func(stored machines.DeviceGrant) bool {
				return stored.Path == remove.Path && stored.Room == remove.Room
			})
		}
	}
	return nil
}

// resolve finds the machine that ref names, by ID first and then by name.
func (a MachineAPI) resolve(ctx context.Context, ref string) (machines.Machine, bool, error) {
	list, err := a.Store.List(ctx)
	if err != nil {
		return machines.Machine{}, false, err
	}
	for _, m := range list {
		if m.ID == ref {
			return m, true, nil
		}
	}
	for _, m := range list {
		if m.Name == ref {
			return m, true, nil
		}
	}
	return machines.Machine{}, false, nil
}

// hostChecksGrants answers one allow or refuse of host checks with the machine's
// grants after it. The owner answers for every room, so allowing replaces the
// grants and refusing clears them. A room root answers for its own room:
// allowing adds its grant and refusing removes only that, so neither touches the
// owner's grant or another room's. The engine refuses every other caller before
// this runs.
func hostChecksGrants(current []machines.HostChecksGrant, actor authority.Actor, allow bool) ([]machines.HostChecksGrant, error) {
	switch actor.Role {
	case authority.Owner:
		if !allow {
			return nil, nil
		}
		return []machines.HostChecksGrant{{By: machines.GrantorOwner}}, nil
	case authority.Root:
		if actor.Room == "" {
			return nil, errors.New("host checks need a room: a room root allows them for its own room")
		}
		mine := machines.HostChecksGrant{By: machines.GrantorRoot, Room: actor.Room}
		if allow {
			return append(slices.Clone(current), mine), nil
		}
		if slices.Contains(current, machines.HostChecksGrant{By: machines.GrantorOwner}) {
			return nil, errors.New("the owner allows host checks on this node for every room: ask the owner to refuse them")
		}
		return slices.DeleteFunc(slices.Clone(current), func(grant machines.HostChecksGrant) bool { return grant == mine }), nil
	}
	return nil, errors.New("SaveNode is owner only: the owner, or a room root for its own room, allows host checks")
}

// ProbeNode reads one machine now and answers with its page after the
// probe, so the owner sees the new health without a second read.
func (a MachineAPI) ProbeNode(ctx context.Context, id string) (MachineDetail, error) {
	m, err := a.find(ctx, id)
	if err != nil {
		return MachineDetail{}, err
	}
	prober := machines.Prober{Store: a.Store, Alerter: a.Alerter, Record: a.RecordProbe}
	if a.Prober != nil {
		prober = *a.Prober
	}
	stored, err := prober.ProbeAndSave(ctx, m)
	if err != nil {
		return MachineDetail{}, err
	}
	return a.machine(ctx, stored, a.ciRuns()), nil
}

// find reads the machine with this ID. The registry is small, so one list
// answers it.
func (a MachineAPI) find(ctx context.Context, id string) (machines.Machine, error) {
	list, err := a.Store.List(ctx)
	if err != nil {
		return machines.Machine{}, err
	}
	for _, m := range list {
		if m.ID == id {
			return m, nil
		}
	}
	return machines.Machine{}, fmt.Errorf("node %q: %w", id, os.ErrNotExist)
}

// machine builds one machine's page. The live figures belong to this engine's
// machine, so only the local row reads them. The CI runs belong to whichever
// machine holds their slots, so a CI machine reports them wherever it runs.
func (a MachineAPI) machine(ctx context.Context, m machines.Machine, ci map[string][]MachineCIRun) MachineDetail {
	out := MachineDetail{MachineSummary: machineSummary(m), SSH: m.SSH, SSHConnectTimeoutSeconds: int(m.ConnectTimeout() / time.Second), HostChecksAllowedBy: a.hostChecksAllowedBy(m)}
	// The probe fills these for every machine, this one included.
	if !m.LastProbe.IsZero() {
		probed := m.LastProbe
		out.ProbedAt = &probed
	}
	if !m.LastSeen.IsZero() {
		seen := m.LastSeen
		out.SeenAt = &seen
		out.Capabilities = a.capabilitiesView(m)
		out.Load = loadView(m.Load)
	}
	runs := ci[m.Name]
	running, waiting := len(runs), 0
	if a.CIQueue != nil {
		running, waiting = a.CIQueue(m.Name)
	}
	// A machine carries the ci label when local CI runs on it, so only that
	// row reports a queue and its runs.
	if m.Enabled && m.HasLabel(machines.LabelCI) {
		out.CI = &MachineCIWork{Running: running, Waiting: waiting}
		out.CIRuns = runs
	}
	if !m.Local {
		// The probe reads the version installed on the machine itself.
		out.DynaVersion = m.Capabilities.DynaVersion
		return out
	}
	out.DynaVersion = a.Version
	if a.Live != nil {
		live := a.Live(ctx)
		out.CPU, out.MemoryPercent, out.Agents, out.Computers = live.CPU, live.MemoryPercent, live.Agents, live.Computers
		out.Harnesses = live.Harnesses
	}
	if a.Running != nil {
		out.Running = a.Running()
	}
	return out
}

// ciRuns reads the runs in flight once, grouped by machine name, so a list of
// machines takes one snapshot instead of one per row.
func (a MachineAPI) ciRuns() map[string][]MachineCIRun {
	if a.CI == nil {
		return nil
	}
	byMachine := map[string][]MachineCIRun{}
	for _, run := range a.CI() {
		byMachine[run.Machine] = append(byMachine[run.Machine], run)
	}
	return byMachine
}

// hostChecksAllowedBy words who allows host checks on m, so the page names the
// room roots whose decision it was.
func (a MachineAPI) hostChecksAllowedBy(m machines.Machine) []string {
	if len(m.HostChecksGrants) == 0 {
		return nil
	}
	out := make([]string, 0, len(m.HostChecksGrants))
	for _, grant := range m.HostChecksGrants {
		if grant.By == machines.GrantorOwner {
			out = append(out, "the owner")
			continue
		}
		room := grant.Room
		if a.RoomName != nil {
			room = a.RoomName(room)
		}
		if room == "" {
			room = "a room"
		}
		out = append(out, "root of room "+room)
	}
	return out
}

// machineSummary is the row a machine's stored state alone gives. A remote
// machine gets its CPU figure from its last probe, so its list row shows the
// same one bar as this engine's own row.
func machineSummary(m machines.Machine) MachineSummary {
	out := MachineSummary{
		ID:              m.ID,
		Name:            m.Name,
		Local:           m.Local,
		Enabled:         m.Enabled,
		Status:          machineStatus(m),
		AllowHostChecks: m.HasHostChecksGrants(),
		Rooms:           m.Rooms,
		Labels:          m.Labels,
		ProbedLabels:    m.ProbedLabels(),
		MaxIOHeavy:      &m.MaxIOHeavy,
		MaxDiskPercent:  &m.MaxDiskPercent,
		Resources:       m.Resources,
	}
	if !m.Local && m.Load.CPUPercent != nil {
		cpu := int(math.Round(*m.Load.CPUPercent))
		out.CPU = &cpu
	}
	if !m.Local && m.Load.MemTotal > 0 {
		used := m.Load.MemTotal - min(m.Load.MemAvailable, m.Load.MemTotal)
		percent := int(math.Round(float64(used) * 100 / float64(m.Load.MemTotal)))
		out.MemoryPercent = &percent
	}
	return out
}

// capabilitiesView words what one probe read for the page. The disk figures
// are the node's one disk rule, which holds every machine's work, so every
// machine the probe read a disk of shows them.
func (a MachineAPI) capabilitiesView(m machines.Machine) *MachineCapabilities {
	c := m.Capabilities
	out := &MachineCapabilities{
		OS:           c.OS,
		Arch:         c.Arch,
		CPUs:         c.CPUs,
		MemTotal:     c.MemTotal,
		MemAvailable: c.MemAvailable,
		GPU:          c.GPU,
		Containers:   c.Containers.Names(),
		Computers:    c.Computers.Ready(),
		Stream:       c.Computers.Stream,
	}
	if c.Disk.Total > 0 {
		free := int64(c.Disk.Free)
		out.FreeDisk = &free
		floor := machines.KeepFreeBytes(m.MaxDiskPercent, c.Disk.Total)
		out.DiskFloor = &floor
		out.DiskKeeps = m.DiskKeeps()
		out.DiskBlock = m.DiskBlock()
	}
	return out
}

// loadView words one load reading for the page.
func loadView(l machines.Load) *MachineLoad {
	out := &MachineLoad{LoadAvg: l.LoadAvg, MemTotal: l.MemTotal, MemAvailable: l.MemAvailable}
	if l.CPUPercent != nil {
		cpu := int(math.Round(*l.CPUPercent))
		out.CPUPercent = &cpu
	}
	return out
}

// machineStatus words a stored machine. This engine's own machine is online
// because it runs the engine; a remote machine shows what its last probe found.
func machineStatus(m machines.Machine) MachineStatus {
	switch {
	case !m.Enabled:
		return MachineDisabled
	case m.Local:
		return MachineOnline
	}
	switch m.Status() {
	case machines.HealthOnline:
		return MachineOnline
	case machines.HealthOffline:
		return MachineOffline
	default:
		return MachineUnknown
	}
}
