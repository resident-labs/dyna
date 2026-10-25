package websvc

import (
	"context"
	"fmt"
	"math"
	"os"
	"strings"
	"time"

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

// MachineCISlots is the CI work a machine runs now, out of its slots.
type MachineCISlots struct {
	Used  int `json:"used"`
	Slots int `json:"slots"`
}

// MachineCIRun is one local CI run in flight on a machine. Title and URL name
// the PR it checks, so the page shows the PR and not only a number.
type MachineCIRun struct {
	Machine string    `json:"machine"`
	Number  int       `json:"number"`
	Title   string    `json:"title"`
	URL     string    `json:"url"`
	Slots   int       `json:"slots"`
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
	CPU             *int            `json:"cpu,omitempty"`
	Agents          *int            `json:"agents,omitempty"`
	Computers       *int            `json:"computers,omitempty"`
	CI              *MachineCISlots `json:"ci,omitempty"`
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
// store checks the result before it writes.
type MachinePatch struct {
	Enabled         *bool            `json:"enabled"`
	Roles           *[]machines.Role `json:"roles"`
	CISlots         *int             `json:"ciSlots"`
	AllowHostChecks *bool            `json:"allowHostChecks"`
	SSH             *string          `json:"ssh"`
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
	// Prober reads a machine when the owner asks for a probe. A nil Prober
	// reads it through this registry with no load sampler of this engine.
	Prober *machines.Prober
	// DiskFloor is the free space a CI machine must keep on its /var/tmp. A
	// nil DiskFloor reads no floor, so every CI machine counts as online.
	DiskFloor func() int64
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

// SaveMachine writes the fields a change names and answers with the page the
// machine has after it.
func (a MachineAPI) SaveMachine(ctx context.Context, id string, patch MachinePatch) (MachineDetail, error) {
	m, err := a.find(ctx, id)
	if err != nil {
		return MachineDetail{}, err
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
	if patch.AllowHostChecks != nil {
		m.AllowHostChecks = *patch.AllowHostChecks
	}
	if patch.SSH != nil {
		m.SSH = strings.TrimSpace(*patch.SSH)
	}
	saved, err := a.Store.Save(ctx, m)
	if err != nil {
		return MachineDetail{}, err
	}
	return a.machine(ctx, saved, a.ciRuns()), nil
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
	out := MachineDetail{MachineSummary: machineSummary(m), SSH: m.SSH}
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
	if m.HasRole(machines.RoleCI) {
		runs := ci[m.Name]
		out.CI = &MachineCISlots{Used: ciUsedSlots(runs), Slots: m.CISlots}
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

// ciUsedSlots adds the slots the runs in flight reserved on one machine.
func ciUsedSlots(runs []MachineCIRun) int {
	used := 0
	for _, run := range runs {
		used += run.Slots
	}
	return used
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
		AllowHostChecks: m.AllowHostChecks,
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
