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

// MachineStatus is what this engine knows about a machine now.
type MachineStatus string

const (
	// MachineOnline is this engine's own machine, or a machine the probe
	// reached: the engine knows it is up.
	MachineOnline MachineStatus = "online"
	// MachineDegraded is a machine that answered a probe but needs attention,
	// such as low free disk space. The probe names the reason.
	MachineDegraded MachineStatus = "degraded"
	// MachineDisabled is a machine switched off in the registry. It takes no
	// new work, but it is not known to be down.
	MachineDisabled MachineStatus = "disabled"
	// MachineOffline is a machine the probe could not reach.
	MachineOffline MachineStatus = "offline"
	// MachineUnknown is a machine the engine does not probe yet.
	MachineUnknown MachineStatus = "unknown"
)

// MachineStatusReason names why a machine is degraded. The page words it, so
// no probe message reaches the screen.
type MachineStatusReason string

const (
	// MachineLowDisk means the machine is short of free disk space.
	MachineLowDisk MachineStatusReason = "low-disk"
)

// MachineCIWork is the CI work a machine runs now and the work that waits
// for room on it, checks and jobs both, from the node's shared queue.
type MachineCIWork struct {
	Running int `json:"running"`
	Waiting int `json:"waiting"`
}

// MachineCIRun is one local CI run in flight on a machine. Title and URL name
// the PR it checks, so the page shows the PR and not only a number.
// MachineCIRunKind is the work a machine run checks. It is watch.RunningCIKind
// as the page contract states it, so a client words a run that has no pull
// request number.
type MachineCIRunKind string

// Machine run kinds.
const (
	MachineCIRunPullRequest MachineCIRunKind = "pull-request"
	MachineCIRunLocalChange MachineCIRunKind = "local-change"
	MachineCIRunSync        MachineCIRunKind = "sync"
)

type MachineCIRun struct {
	Machine string `json:"machine"`
	Number  int    `json:"number"`
	// Kind is the work the run checks, which a client words.
	Kind MachineCIRunKind `json:"kind"`
	// Branch names a local change's branch, which has no pull request number.
	Branch  string    `json:"branch,omitempty"`
	Title   string    `json:"title"`
	URL     string    `json:"url"`
	Trial   bool      `json:"trial"`
	Suites  []string  `json:"suites,omitempty"`
	Started time.Time `json:"started"`
}

// MachineAgent is a session that runs on this engine's machine now.
type MachineAgent struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Address string `json:"address,omitempty"`
	Room    string `json:"room,omitempty"`
	RoomID  string `json:"roomId,omitempty"`
	Hue     int    `json:"hue"`
}

// MachineSummary is one row of the Machines list. A figure the engine does not
// measure stays nil, so the client shows no count instead of a zero.
type MachineSummary struct {
	ID              string          `json:"id"`
	Name            string          `json:"name"`
	Roles           []machines.Role `json:"roles"`
	Local           bool            `json:"local"`
	Enabled         bool            `json:"enabled"`
	Status          MachineStatus   `json:"status"`
	CISlots         int             `json:"ciSlots"`
	AllowHostChecks bool            `json:"allowHostChecks"`
	// MaxIOHeavy and MaxDiskPercent are the node queue's stored limits; zero
	// keeps each default.
	MaxIOHeavy     *int                          `json:"maxIOHeavy,omitempty"`
	MaxDiskPercent *int                          `json:"maxDiskPercent,omitempty"`
	Resources      map[machines.ResourceName]int `json:"resources,omitempty"`
	Rooms          []string                      `json:"rooms,omitempty"`
	// Suites limits the local CI work the machine takes to those suites; empty
	// means every suite.
	Suites    []string       `json:"suites,omitempty"`
	CPU       *int           `json:"cpu,omitempty"`
	Agents    *int           `json:"agents,omitempty"`
	Computers *int           `json:"computers,omitempty"`
	CI        *MachineCIWork `json:"ci,omitempty"`
}

// MachineWorkItem is one unit of work on a machine, with the names the page
// shows for its room and session. A name stays empty when the engine cannot
// resolve it, and the page then leaves it out.
type MachineWorkItem struct {
	jobs.WorkItem
	RoomName    string `json:"roomName,omitempty"`
	SessionName string `json:"sessionName,omitempty"`
}

// MachineWork is the Work section of the Machine page: what the machine's
// queue counts against, the work it runs and the work that waits, in
// admission order. It comes from one read of the admission rows and the jobs
// table.
type MachineWork struct {
	Capacity jobs.NodeCapacity `json:"capacity"`
	Running  []MachineWorkItem `json:"running"`
	Waiting  []MachineWorkItem `json:"waiting"`
}

// MachineHarness is one agent harness installed on a machine and its version.
type MachineHarness struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// MachineDetail is the Machine page: the summary, how the engine reaches the
// machine, the versions this machine runs, the sessions that run on it, and
// what the last probe read.
type MachineDetail struct {
	MachineSummary
	SSH         string `json:"ssh"`
	DynaVersion string `json:"dynaVersion,omitempty"`
	// SSHConnectTimeoutSeconds is how long an ssh to the machine waits for the
	// connection: the stored value, or the default when none is stored.
	SSHConnectTimeoutSeconds int `json:"sshConnectTimeoutSeconds,omitempty"`
	// HostChecksAllowedBy words who allows host checks, when someone does:
	// "the owner", or "root of room <name>".
	HostChecksAllowedBy []string `json:"hostChecksAllowedBy,omitempty"`

	Harnesses []MachineHarness `json:"harnesses,omitempty"`
	Running   []MachineAgent   `json:"running,omitempty"`
	// CIRuns lists the CI runs in flight on this machine, so the page shows
	// what it checks now. The count of them is ci.used.
	CIRuns []MachineCIRun `json:"ciRuns,omitempty"`

	// ProbedAt is when the engine last read the machine, and SeenAt when a
	// probe last reached it. Both stay absent before the first probe, and
	// Capabilities and Load are what that good probe read. A failed probe
	// keeps them, so the page shows what the machine has while it is offline.
	ProbedAt     *time.Time           `json:"probedAt,omitempty"`
	SeenAt       *time.Time           `json:"seenAt,omitempty"`
	Capabilities *MachineCapabilities `json:"capabilities,omitempty"`
	Load         *MachineLoad         `json:"load,omitempty"`
	// StatusReason names why a degraded machine needs attention. It stays
	// empty for every other status.
	StatusReason MachineStatusReason `json:"statusReason,omitempty"`
}

// MachineCapabilities is what the last probe read about a machine. Memory
// values are bytes.
type MachineCapabilities struct {
	OS           string   `json:"os"`
	Arch         string   `json:"arch"`
	CPUs         int      `json:"cpus"`
	MemTotal     uint64   `json:"memTotal"`
	MemAvailable uint64   `json:"memAvailable"`
	GPU          bool     `json:"gpu"`
	Containers   []string `json:"containers,omitempty"`
	// Computers is whether the machine can host an agent computer, which needs
	// labwc and Xwayland. Stream is whether wayvnc can show its desktop in the
	// app.
	Computers bool `json:"computers"`
	Stream    bool `json:"stream"`
	// FreeDisk is the free space of the filesystem that holds the machine's
	// /var/tmp, where Dyna keeps its CI work, and DiskFloor is the free space a
	// CI machine must keep. Values are bytes, and both stay absent for a
	// machine that is not a CI machine or whose probe could not read them.
	FreeDisk  *int64 `json:"freeDisk,omitempty"`
	DiskFloor *int64 `json:"diskFloor,omitempty"`
}

// MachineLoad is the last load reading of a machine. CPUPercent stays absent
// when the probe could not take two readings.
type MachineLoad struct {
	CPUPercent   *int       `json:"cpuPercent,omitempty"`
	LoadAvg      [3]float64 `json:"loadAvg"`
	MemTotal     uint64     `json:"memTotal"`
	MemAvailable uint64     `json:"memAvailable"`
}

// MachinePatch is one change to a machine. A nil field keeps its value; the
// store checks the result before it writes. Create adds the machine that the
// reference names when the registry has none.
type MachinePatch struct {
	Name            *string          `json:"name,omitempty"`
	Enabled         *bool            `json:"enabled"`
	Roles           *[]machines.Role `json:"roles"`
	CISlots         *int             `json:"ciSlots"`
	AllowHostChecks *bool            `json:"allowHostChecks"`
	Rooms           *[]string        `json:"rooms"`
	Suites          *[]string        `json:"suites"`
	SSH             *string          `json:"ssh"`
	Create          bool             `json:"create,omitempty"`
	// Remove deletes the machine that the reference names.
	Remove bool `json:"remove,omitempty"`
	// ContainerSocket is the container runtime socket a workflow job uses on
	// this machine, as a DOCKER_HOST value; an empty value means Docker's.
	ContainerSocket *string `json:"containerSocket"`
	// SSHConnectTimeoutSeconds is how long an ssh to this machine waits for the
	// connection; zero restores the default.
	SSHConnectTimeoutSeconds *int `json:"sshConnectTimeoutSeconds,omitempty"`
	// CheckTmp is the folder that holds each host check's temporary folder on
	// this machine; an empty value means the node's temporary directory.
	CheckTmp *string `json:"checkTmp,omitempty"`
	// MaxIOHeavy caps how many IO-heavy jobs and checks this node runs at
	// once; zero keeps the default of one per sixteen probed CPUs.
	MaxIOHeavy *int `json:"maxIOHeavy,omitempty"`
	// MaxDiskPercent is the used-disk percent above which this node admits no
	// new work; zero keeps the default of 90.
	MaxDiskPercent *int `json:"maxDiskPercent,omitempty"`
	// Resources replaces the node's declared named resource counts.
	Resources *map[machines.ResourceName]int `json:"resources,omitempty"`
}

// MachineLive is what this engine's own machine uses now. The engine fills
// only what it can read, so a figure without a sample stays nil.
type MachineLive struct {
	CPU       *int
	Agents    *int
	Computers *int
	Harnesses []MachineHarness
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
	Prober *machines.Prober
	// DiskFloor is the free space a CI machine must keep on its /var/tmp. A
	// nil DiskFloor reads no floor, so every CI machine counts as online.
	DiskFloor func() int64
	// RoomName names a room from its ID, so the page says which room allows
	// host checks and not only its ID. A nil RoomName shows the ID.
	RoomName func(roomID string) string
	// Alerter posts the low-disk alert of a machine that starts holding CI. A
	// nil Alerter posts nothing.
	Alerter machines.Alerter
}

// Machines lists every machine, the engine host first.
func (a MachineAPI) Machines(ctx context.Context) ([]MachineSummary, error) {
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

// Machine reads one machine's page by its ID.
func (a MachineAPI) Machine(ctx context.Context, id string) (MachineDetail, error) {
	m, err := a.find(ctx, id)
	if err != nil {
		return MachineDetail{}, err
	}
	return a.machine(ctx, m, a.ciRuns()), nil
}

// MachineWork reads the Work section of one machine's page by its ID.
func (a MachineAPI) MachineWork(ctx context.Context, id string) (MachineWork, error) {
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

// SaveMachine applies one change to the machine that ref names, by ID or by
// name, and answers with the page the machine has after it. It is the one
// machine mutation path for the web, the CLI and the CI-node alias: it
// normalizes the result and the store validates it before it writes.
func (a MachineAPI) SaveMachine(ctx context.Context, ref string, patch MachinePatch) (MachineDetail, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return MachineDetail{}, errors.New("a machine reference is required")
	}
	m, found, err := a.resolve(ctx, ref)
	if err != nil {
		return MachineDetail{}, err
	}
	if patch.Remove {
		if !found {
			return MachineDetail{}, fmt.Errorf("machine %q: %w", ref, os.ErrNotExist)
		}
		removed := a.machine(ctx, m, a.ciRuns())
		if err := a.Store.Remove(ctx, m.Name); err != nil {
			return MachineDetail{}, err
		}
		return removed, nil
	}
	if found && patch.Create {
		return MachineDetail{}, fmt.Errorf("machine %s exists; use dyna machine edit", m.Name)
	}
	if !found {
		if !patch.Create {
			return MachineDetail{}, fmt.Errorf("machine %q: %w", ref, os.ErrNotExist)
		}
		m = machines.Machine{Name: ref, Enabled: true}
	}
	if err := applyMachinePatch(&m, patch); err != nil {
		return MachineDetail{}, err
	}
	saved, err := a.Store.Save(ctx, m)
	if err != nil {
		return MachineDetail{}, err
	}
	if patch.AllowHostChecks != nil {
		// The grant changes inside the store's writer transaction, so this
		// answer cannot restore a grant that another caller refused meanwhile.
		if _, err := a.Store.UpdateHostChecksGrants(ctx, saved.ID, func(stored machines.Machine) ([]machines.HostChecksGrant, error) {
			return hostChecksGrants(stored.HostChecksGrants, authority.Of(ctx), *patch.AllowHostChecks)
		}); err != nil {
			return MachineDetail{}, err
		}
	}
	stored, err := a.find(ctx, saved.ID)
	if err != nil {
		return MachineDetail{}, err
	}
	return a.machine(ctx, stored, a.ciRuns()), nil
}

// applyMachinePatch writes the fields the patch names and keeps the rules the
// fields imply: the ci role needs a slot, and rooms, suites and host checks
// belong to a machine that holds CI work.
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
	if patch.Roles != nil {
		m.Roles = *patch.Roles
	}
	if patch.CISlots != nil {
		m.CISlots = *patch.CISlots
	}
	if patch.Rooms != nil {
		m.Rooms = *patch.Rooms
	}
	if patch.Suites != nil {
		m.Suites = *patch.Suites
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
	if m.HasRole(machines.RoleCI) && m.CISlots < 1 && patch.CISlots == nil {
		m.CISlots = 1
	}
	if patch.Rooms != nil && len(*patch.Rooms) > 0 && !m.HasRole(machines.RoleCI) {
		return errors.New("a room list requires the ci role")
	}
	if patch.Suites != nil && len(*patch.Suites) > 0 && !m.HasRole(machines.RoleCI) {
		return errors.New("a suite list requires the ci role")
	}
	if patch.AllowHostChecks != nil && *patch.AllowHostChecks && !m.HasRole(machines.RoleCI) {
		return errors.New("host checks require the ci role")
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
			return nil, errors.New("the owner allows host checks on this machine for every room: ask the owner to refuse them")
		}
		return slices.DeleteFunc(slices.Clone(current), func(grant machines.HostChecksGrant) bool { return grant == mine }), nil
	}
	return nil, errors.New("SaveMachine is owner only: the owner, or a room root for its own room, allows host checks")
}

// ProbeMachine reads one machine now and answers with its page after the
// probe, so the owner sees the new health without a second read.
func (a MachineAPI) ProbeMachine(ctx context.Context, id string) (MachineDetail, error) {
	m, err := a.find(ctx, id)
	if err != nil {
		return MachineDetail{}, err
	}
	prober := machines.Prober{Store: a.Store, DiskFloorBytes: a.floorBytes(), Alerter: a.Alerter}
	if a.Prober != nil {
		prober = *a.Prober
	}
	stored, err := prober.ProbeAndSave(ctx, m)
	if err != nil {
		return MachineDetail{}, err
	}
	return a.machine(ctx, stored, a.ciRuns()), nil
}

// floorBytes is the disk floor as the prober reads it.
func (a MachineAPI) floorBytes() uint64 {
	if a.DiskFloor == nil {
		return 0
	}
	if floor := a.DiskFloor(); floor > 0 {
		return uint64(floor)
	}
	return 0
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
	return machines.Machine{}, fmt.Errorf("machine %q: %w", id, os.ErrNotExist)
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
	// A CI machine always shows its queue; any other machine shows it while
	// jobs run or wait on it.
	if m.HasRole(machines.RoleCI) || running+waiting > 0 {
		out.CI = &MachineCIWork{Running: running, Waiting: waiting}
		out.CIRuns = runs
	}
	// The reason explains a degraded health and nothing else, so a machine
	// that recovered loses it.
	if m.Status() == machines.HealthDegraded {
		out.StatusReason = statusReason(m.HealthReason)
	}
	if !m.Local {
		// The probe reads the version installed on the machine itself.
		out.DynaVersion = m.Capabilities.DynaVersion
		return out
	}
	out.DynaVersion = a.Version
	if a.Live != nil {
		live := a.Live(ctx)
		out.CPU, out.Agents, out.Computers = live.CPU, live.Agents, live.Computers
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
		Roles:           m.Roles,
		Local:           m.Local,
		Enabled:         m.Enabled,
		Status:          machineStatus(m),
		CISlots:         m.CISlots,
		AllowHostChecks: m.HasHostChecksGrants(),
		Rooms:           m.Rooms,
		Suites:          m.Suites,
		MaxIOHeavy:      &m.MaxIOHeavy,
		MaxDiskPercent:  &m.MaxDiskPercent,
		Resources:       m.Resources,
	}
	if !m.Local && m.Load.CPUPercent != nil {
		cpu := int(math.Round(*m.Load.CPUPercent))
		out.CPU = &cpu
	}
	return out
}

// capabilitiesView words what one probe read for the page. The disk figures
// belong to a CI machine, whose /var/tmp holds Dyna's CI work, so no other
// machine shows them.
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
	if m.HasRole(machines.RoleCI) && c.Disk.Total > 0 {
		free := int64(c.Disk.Free)
		out.FreeDisk = &free
		if a.DiskFloor != nil {
			floor := a.DiskFloor()
			out.DiskFloor = &floor
		}
	}
	return out
}

// statusReason words the stored health reason as the page's reason. A reason
// the page does not know reads as empty, so it says only that the machine
// needs attention.
func statusReason(reason machines.HealthReason) MachineStatusReason {
	if reason == machines.ReasonLowDisk {
		return MachineLowDisk
	}
	return ""
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
// A probe that answers but needs attention reports a degraded health, which
// maps to MachineDegraded with the reason the probe names.
func machineStatus(m machines.Machine) MachineStatus {
	switch {
	case !m.Enabled:
		return MachineDisabled
	case m.Status() == machines.HealthDegraded:
		// A machine that answers but holds its CI work needs attention whether
		// it is this engine's machine or a remote one.
		return MachineDegraded
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
