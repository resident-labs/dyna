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
	// MachineOnline is this engine's own machine: the call runs on it.
	MachineOnline MachineStatus = "online"
	// MachineOff is a machine switched off in the registry.
	MachineOff MachineStatus = "off"
	// MachineUnknown is a machine the engine does not probe yet.
	MachineUnknown MachineStatus = "unknown"
)

// MachineCISlots is the CI work a machine runs now, out of its slots.
type MachineCISlots struct {
	Used  int `json:"used"`
	Slots int `json:"slots"`
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
// machine, and the versions this machine runs.
type MachineDetail struct {
	MachineSummary
	SSH         string           `json:"ssh"`
	CISlots     int              `json:"ciSlots"`
	DynaVersion string           `json:"dynaVersion,omitempty"`
	Harnesses   []MachineHarness `json:"harnesses,omitempty"`
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
	out := MachineDetail{MachineSummary: machineSummary(m), SSH: m.SSH, CISlots: m.CISlots}
	if !m.Local {
		return out
	}
	out.DynaVersion = a.Version
	if a.Live == nil {
		return out
	}
	live := a.Live(ctx)
	out.CPU, out.Agents, out.Computers = live.CPU, live.Agents, live.Computers
	out.Harnesses = live.Harnesses
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
	}
}

// machineStatus words a stored machine: this one runs the engine, a switched
// off one runs nothing, and a remote one is not probed yet.
func machineStatus(m machines.Machine) MachineStatus {
	switch {
	case !m.Enabled:
		return MachineOff
	case m.Local:
		return MachineOnline
	default:
		return MachineUnknown
	}
}
