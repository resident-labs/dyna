package websvc

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"slices"
	"testing"
	"time"

	"dyna-agent/internal/callspec"
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
		Running: func() []MachineAgent {
			return []MachineAgent{{ID: "ses_a", Name: "lead", Address: "dyna:lead", Room: "dyna", RoomID: "room_1", Hue: 3}}
		},
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

// probeOutput is what one probe prints on a Linux machine.
const probeOutput = `os=Linux
arch=x86_64
cpus=20
memtotal=32700000000
memavailable=26000000000
load1=0.10
load5=0.20
load15=0.30
idle=900
ticks=1000
gpu=yes
has_docker=yes
has_labwc=yes
has_Xwayland=yes
dyna=dyna 0.4.3`

// A probe from the page reads the machine, stores what it found and answers
// with the page after it. A failed probe keeps what the last good one read.
func TestProbeMachineStoresWhatItRead(t *testing.T) {
	api := machineAPI(t)
	remote, err := api.Store.Save(t.Context(), machines.Machine{Name: "hermes", Roles: []machines.Role{machines.RoleCI}, SSH: "pip@hermes", CISlots: 4, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	prints := func(_ context.Context, _ string, _ io.Reader, out, _ io.Writer, _ string, _ ...string) error {
		_, err := io.WriteString(out, probeOutput)
		return err
	}
	api.Prober = &machines.Prober{Store: api.Store, Command: prints}

	got, err := api.ProbeMachine(t.Context(), remote.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != MachineOnline || got.Capabilities == nil || got.Load == nil {
		t.Fatalf("probed machine = %+v, want online with a reading", got)
	}
	if got.Capabilities.CPUs != 20 || !got.Capabilities.Computers || got.Capabilities.Stream || !got.Capabilities.GPU {
		t.Fatalf("capabilities = %+v", got.Capabilities)
	}
	if len(got.Capabilities.Containers) != 1 || got.Capabilities.Containers[0] != "docker" {
		t.Fatalf("containers = %v, want docker", got.Capabilities.Containers)
	}
	if got.Load.CPUPercent == nil || *got.Load.CPUPercent != 10 || got.Load.MemAvailable != 26000000000 {
		t.Fatalf("load = %+v, want a 10 percent reading", got.Load)
	}
	if got.SeenAt == nil || got.ProbedAt == nil || got.DynaVersion != "dyna 0.4.3" {
		t.Fatalf("probe times = %v, %v, version %q", got.ProbedAt, got.SeenAt, got.DynaVersion)
	}

	// A probe that cannot reach the machine keeps the capabilities and marks
	// it offline, which is what the list shows too.
	api.Prober.Command = func(context.Context, string, io.Reader, io.Writer, io.Writer, string, ...string) error {
		return errors.New("ssh: connect to host pip@hermes port 22: Connection refused")
	}
	down, err := api.ProbeMachine(t.Context(), remote.ID)
	if err != nil {
		t.Fatal(err)
	}
	if down.Status != MachineOffline {
		t.Fatalf("status after a failed probe = %s", down.Status)
	}
	if down.Capabilities == nil || down.Capabilities.CPUs != 20 || down.SeenAt == nil {
		t.Fatalf("failed probe lost the reading: %+v", down)
	}
	list, err := api.Machines(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if list[len(list)-1].Status != MachineOffline {
		t.Fatalf("list status after a failed probe = %s", list[len(list)-1].Status)
	}
}

// The bound probe call must outlast the probe itself. With the 5 s default
// deadline, a slow SSH connection was cancelled before the probe could store
// that the machine is offline, so the page kept its old health.
func TestProbeMachineCallHasRoomForSSH(t *testing.T) {
	api := machineAPI(t)
	remote, err := api.Store.Save(t.Context(), machines.Machine{Name: "hermes", Roles: []machines.Role{machines.RoleCI}, SSH: "pip@hermes", CISlots: 4, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	var left time.Duration
	api.Prober = &machines.Prober{Store: api.Store, Command: func(ctx context.Context, _ string, _ io.Reader, out, _ io.Writer, _ string, _ ...string) error {
		if end, ok := ctx.Deadline(); ok {
			left = time.Until(end)
		}
		_, err := io.WriteString(out, probeOutput)
		return err
	}}
	calls, err := Bind([]Binding{{Name: "ProbeMachine", Access: Write, Scope: ScopeAdmin, Method: api.ProbeMachine}})
	if err != nil {
		t.Fatal(err)
	}
	id, err := json.Marshal(remote.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := calls["ProbeMachine"](t.Context(), []json.RawMessage{id}); err != nil {
		t.Fatal(err)
	}
	// The probe takes its 20 s budget from the call's deadline, so a shorter
	// call deadline clamps it. That is what made a slow SSH connection reach
	// the request deadline and store nothing.
	if left < 19*time.Second {
		t.Fatalf("the probe ran with %v left, so the call deadline clamped its 20 s budget", left)
	}
	if d := callspec.Deadline("ProbeMachine"); d < 25*time.Second {
		t.Fatalf("ProbeMachine has a %v deadline, which cannot cover a 20 s probe and its store", d)
	}
	page, err := api.Machine(t.Context(), remote.ID)
	if err != nil {
		t.Fatal(err)
	}
	if page.Status != MachineOnline || page.SeenAt == nil {
		t.Fatalf("page after the bound probe = %+v", page)
	}
}
