package websvc

import (
	"context"
	"errors"
	"os"
	"slices"
	"testing"

	"dyna-agent/internal/machines"
)

// machineAPI serves a fresh registry whose local machine reports fixed live
// figures, so a test can tell a measured zero from no figure at all.
func machineAPI(t *testing.T) MachineAPI {
	t.Helper()
	dir := t.TempDir()
	if err := machines.Migrate(dir); err != nil {
		t.Fatal(err)
	}
	cpu, agents, computers := 42, 3, 1
	return MachineAPI{
		Store:   machines.Store{DataDir: dir},
		Version: "1.2.3",
		Live: func(context.Context) MachineLive {
			return MachineLive{CPU: &cpu, Agents: &agents, Computers: &computers, Harnesses: []MachineHarness{{Name: "pi", Version: "1.4.0"}}}
		},
		Running: func() []MachineAgent { return []MachineAgent{{ID: "ses_a", Name: "lead", Address: "dyna:lead", Room: "dyna", RoomID: "room_1", Hue: 3}} },
	}
}

func TestMachinesListAndPatch(t *testing.T) {
	api := machineAPI(t)

	list, err := api.Machines(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || !list[0].Local {
		t.Fatalf("machines = %+v, want the engine host only", list)
	}
	local := list[0]
	if local.Name == "" || local.Status != MachineOnline {
		t.Fatalf("local machine = %+v, want a name and the online status", local)
	}
	if !slices.Contains(local.Roles, machines.RoleEngine) || !slices.Contains(local.Roles, machines.RoleAgents) {
		t.Fatalf("local roles = %v, want engine and agents", local.Roles)
	}
	if local.CPU == nil || *local.CPU != 42 || local.Agents == nil || *local.Agents != 3 || local.Computers == nil || *local.Computers != 1 {
		t.Fatalf("live figures = %+v, want 42 percent and 3 agents and 1 computer", local)
	}
	if local.CI != nil {
		t.Fatalf("ci = %+v, want no slot use before the CI PR", local.CI)
	}

	detail, err := api.Machine(t.Context(), local.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.DynaVersion != "1.2.3" || len(detail.Harnesses) != 1 || detail.SSH != "" || detail.CISlots != 0 {
		t.Fatalf("detail = %+v, want the version, one harness and no route", detail)
	}
	if len(detail.Running) != 1 || detail.Running[0].Name != "lead" || detail.Running[0].Address != "dyna:lead" {
		t.Fatalf("running = %+v, want the named session", detail.Running)
	}

	// A machine other than this one stores its route and reports no figure the
	// engine has not probed.
	remote, err := api.Store.Save(t.Context(), machines.Machine{Name: "hermes", Roles: []machines.Role{machines.RoleCI}, SSH: "pip@hermes", CISlots: 4, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	got, err := api.Machine(t.Context(), remote.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != MachineUnknown || got.Agents != nil || got.CPU != nil || got.CISlots != 4 || got.SSH != "pip@hermes" {
		t.Fatalf("remote = %+v, want unknown with its stored route and slots", got)
	}
	if got.Running != nil {
		t.Fatalf("remote running = %+v; this engine cannot name its sessions", got.Running)
	}

	// A patch takes the fields it names and the store refuses a machine that no
	// longer validates.
	off := false
	disabled, err := api.SaveMachine(t.Context(), local.ID, MachinePatch{Enabled: &off})
	if err != nil {
		t.Fatal(err)
	}
	if disabled.Enabled || disabled.Status != MachineDisabled {
		t.Fatalf("disabled = %+v, want the disabled status", disabled)
	}
	if _, err := api.SaveMachine(t.Context(), local.ID, MachinePatch{Roles: &[]machines.Role{machines.RoleAgents}}); !errors.Is(err, machines.ErrLocal) {
		t.Fatalf("removing the engine role = %v, want ErrLocal", err)
	}
	slots := 8
	kept, err := api.SaveMachine(t.Context(), local.ID, MachinePatch{CISlots: &slots})
	if err != nil || kept.CISlots != 0 {
		t.Fatalf("slots without the ci role = %d, %v; want 0", kept.CISlots, err)
	}
	if _, err := api.Machine(t.Context(), "m_missing"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unknown machine = %v, want os.ErrNotExist", err)
	}
}
