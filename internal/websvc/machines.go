package websvc

import (
	"context"
	"fmt"
	"os"
	"strings"

	"dyna-agent/internal/machines"
)

// MachineStatus is what this engine knows about a machine now.
type MachineStatus string

const (
	// MachineOnline is this engine's own machine, or a machine the probe
	// reached: the engine knows it is up.
	MachineOnline MachineStatus = "online"
	// MachineDisabled is a machine switched off in the registry. It takes no
	// new work, but it is not known to be down.
	MachineDisabled MachineStatus = "disabled"
	// MachineOffline is a machine the probe could not reach.
	MachineOffline MachineStatus = "offline"
	// MachineUnknown is a machine the engine does not probe yet.
	MachineUnknown MachineStatus = "unknown"
)

// MachineCISlots is the CI work a machine runs now, out of its slots.
type MachineCISlots struct {
	Used  int `json:"used"`
	Slots int `json:"slots"`
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
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Roles     []machines.Role `json:"roles"`
	Local     bool            `json:"local"`
	Enabled   bool            `json:"enabled"`
	Status    MachineStatus   `json:"status"`
	CISlots   int             `json:"ciSlots"`
	CPU       *int            `json:"cpu,omitempty"`
	Agents    *int            `json:"agents,omitempty"`
	Computers *int            `json:"computers,omitempty"`
	CI        *MachineCISlots `json:"ci,omitempty"`
}

// MachineHarness is one agent harness installed on a machine and its version.
type MachineHarness struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// MachineDetail is the Machine page: the summary, how the engine reaches the
// machine, the versions this machine runs and the sessions that run on it.
type MachineDetail struct {
	MachineSummary
	SSH         string `json:"ssh"`
	DynaVersion string `json:"dynaVersion,omitempty"`

	Harnesses []MachineHarness `json:"harnesses,omitempty"`
	Running   []MachineAgent   `json:"running,omitempty"`
}

// MachinePatch is one change to a machine. A nil field keeps its value; the
// store checks the result before it writes.
type MachinePatch struct {
	Enabled *bool            `json:"enabled"`
	Roles   *[]machines.Role `json:"roles"`
	CISlots *int             `json:"ciSlots"`
	SSH     *string          `json:"ssh"`
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
}

// Machines lists every machine, the engine host first.
func (a MachineAPI) Machines(ctx context.Context) ([]MachineSummary, error) {
	list, err := a.Store.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]MachineSummary, 0, len(list))
	for _, m := range list {
		out = append(out, a.machine(ctx, m).MachineSummary)
	}
	return out, nil
}

// Machine reads one machine's page by its ID.
func (a MachineAPI) Machine(ctx context.Context, id string) (MachineDetail, error) {
	m, err := a.find(ctx, id)
	if err != nil {
		return MachineDetail{}, err
	}
	return a.machine(ctx, m), nil
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
	if patch.SSH != nil {
		m.SSH = strings.TrimSpace(*patch.SSH)
	}
	saved, err := a.Store.Save(ctx, m)
	if err != nil {
		return MachineDetail{}, err
	}
	return a.machine(ctx, saved), nil
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
// machine, so only the local row reads them.
func (a MachineAPI) machine(ctx context.Context, m machines.Machine) MachineDetail {
	out := MachineDetail{MachineSummary: machineSummary(m), SSH: m.SSH}
	if !m.Local {
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

// machineSummary is the row a machine's stored state alone gives.
func machineSummary(m machines.Machine) MachineSummary {
	return MachineSummary{
		ID:      m.ID,
		Name:    m.Name,
		Roles:   m.Roles,
		Local:   m.Local,
		Enabled: m.Enabled,
		Status:  machineStatus(m),
		CISlots: m.CISlots,
	}
}

// machineStatus words a stored machine: this one runs the engine, a switched
// off one takes no new work, and a remote one is not probed yet.
func machineStatus(m machines.Machine) MachineStatus {
	switch {
	case !m.Enabled:
		return MachineDisabled
	case m.Local:
		return MachineOnline
	default:
		return MachineUnknown
	}
}
