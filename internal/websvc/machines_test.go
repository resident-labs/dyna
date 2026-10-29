package websvc

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/callspec"
	"dyna-agent/internal/jobs"
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
	// The engine host carries the ci label, so it reports its queue.
	engineHost, err := api.Store.GetLocal(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	engineHost.Labels = []string{machines.LabelCI}
	if _, err := api.Store.Save(t.Context(), engineHost); err != nil {
		t.Fatal(err)
	}

	list, err := api.Nodes(t.Context())
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
	if local.CPU == nil || *local.CPU != 42 || local.Agents == nil || *local.Agents != 3 || local.Computers == nil || *local.Computers != 1 {
		t.Fatalf("live figures = %+v, want 42 percent and 3 agents and 1 computer", local)
	}
	if local.CI == nil || local.CI.Running != 0 || local.CI.Waiting != 0 {
		t.Fatalf("ci = %+v, want an empty queue figure for the ci-labeled engine host", local.CI)
	}

	detail, err := api.Node(t.Context(), local.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.DynaVersion != "1.2.3" || len(detail.Harnesses) != 1 || detail.SSH != "" {
		t.Fatalf("detail = %+v, want the version, one harness and no route", detail)
	}
	if len(detail.Running) != 1 || detail.Running[0].Name != "lead" || detail.Running[0].Address != "dyna:lead" {
		t.Fatalf("running = %+v, want the named session", detail.Running)
	}

	// A machine other than this one stores its route and reports no figure the
	// engine has not probed.
	remote, err := api.Store.Save(t.Context(), machines.Machine{Name: "hermes", SSH: "pip@hermes", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	got, err := api.Node(t.Context(), remote.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != MachineUnknown || got.Agents != nil || got.CPU != nil || got.SSH != "pip@hermes" {
		t.Fatalf("remote = %+v, want unknown with its stored route", got)
	}
	if got.Running != nil {
		t.Fatalf("remote running = %+v; this engine cannot name its sessions", got.Running)
	}

	// A patch takes the fields it names and the store refuses a machine that no
	// longer validates.
	off := false
	disabled, err := api.SaveNode(t.Context(), local.ID, MachinePatch{Enabled: &off})
	if err != nil {
		t.Fatal(err)
	}
	if disabled.Enabled || disabled.Status != MachineDisabled {
		t.Fatalf("disabled = %+v, want the disabled status", disabled)
	}
	if _, err := api.Node(t.Context(), "m_missing"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unknown machine = %v, want os.ErrNotExist", err)
	}
}

func TestContainerRegistrationPatchPreservesOtherRooms(t *testing.T) {
	api := machineAPI(t)
	node, err := api.Store.Save(t.Context(), machines.Machine{
		Name: "pve", SSH: "pip@pve", Enabled: true,
		Mounts:  []machines.NamedMount{{Name: "other", Source: "/srv/other", Room: "room_other", Mode: machines.MountRO}},
		Devices: []machines.DeviceGrant{{Path: "/dev/other", Room: "room_other"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	root := authority.WithActor(t.Context(), authority.Actor{Role: authority.Root, Session: "root", Room: "room_dyna"})
	mounts := []machines.NamedMount{{Name: "data", Source: "/srv/data", Room: "room_dyna", Mode: machines.MountRO}}
	devices := []machines.DeviceGrant{{Path: "/dev/dri", Room: "room_dyna"}}
	if _, err := api.SaveNode(root, node.ID, MachinePatch{Mounts: &mounts, Devices: &devices}); err != nil {
		t.Fatal(err)
	}
	stored, err := api.Store.GetByID(t.Context(), node.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Mounts) != 2 || len(stored.Devices) != 2 {
		t.Fatalf("an own-room patch replaced another room's registrations: %#v, %#v", stored.Mounts, stored.Devices)
	}
}

func TestContainerRegistrationRemovalRejectsNewJobs(t *testing.T) {
	api := machineAPI(t)
	node, err := api.Store.Save(t.Context(), machines.Machine{
		Name: "pve", SSH: "pip@pve", Enabled: true,
		Mounts: []machines.NamedMount{
			{Name: "data", Source: "/srv/data", Room: "room_dyna", Mode: machines.MountRO},
			{Name: "other", Source: "/srv/other", Room: "room_other", Mode: machines.MountRO},
		},
		Devices: []machines.DeviceGrant{
			{Path: "/dev/dri", Room: "room_dyna"},
			{Path: "/dev/other", Room: "room_other"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	root := authority.WithActor(t.Context(), authority.Actor{Role: authority.Root, Session: "root", Room: "room_dyna"})
	removeMounts := []machines.MountRef{{Name: "data", Room: "room_dyna"}}
	removeDevices := []machines.DeviceRef{{Path: "/dev/dri", Room: "room_dyna"}}
	if _, err := api.SaveNode(root, node.ID, MachinePatch{RemoveMounts: &removeMounts, RemoveDevices: &removeDevices}); err != nil {
		t.Fatal(err)
	}
	stored, err := api.Store.GetByID(t.Context(), node.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Mounts) != 1 || stored.Mounts[0].Room != "room_other" || len(stored.Devices) != 1 || stored.Devices[0].Room != "room_other" {
		t.Fatalf("revocation changed another room's registrations: %#v, %#v", stored.Mounts, stored.Devices)
	}
	svc := &jobs.Service{Store: jobs.Store{DataDir: api.Store.DataDir}, Machines: api.Store, DataDir: api.Store.DataDir}
	_, err = svc.Submit(t.Context(), jobs.Input{Node: node.Name, RoomID: "room_dyna", Command: []string{"true"}, Image: "alpine", Mounts: []jobs.MountRequest{{Name: "data", Target: "/data"}}})
	if err == nil || !strings.Contains(err.Error(), "unknown mount") {
		t.Fatalf("revoked mount submission = %v, want an unknown-mount refusal", err)
	}
	_, err = svc.Submit(t.Context(), jobs.Input{Node: node.Name, RoomID: "room_dyna", Command: []string{"true"}, Image: "alpine", Devices: []string{"/dev/dri"}})
	if err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("revoked device submission = %v, want an allow-list refusal", err)
	}
	if _, err := svc.Submit(t.Context(), jobs.Input{
		Node: node.Name, RoomID: "room_other", Command: []string{"true"}, Image: "alpine",
		Mounts: []jobs.MountRequest{{Name: "other", Target: "/data"}}, Devices: []string{"/dev/other"},
	}); err != nil {
		t.Fatalf("another room lost its registration: %v", err)
	}
}

// A room root allows host checks for its own room, so the page names that room
// and another room's root cannot take the owner's grant away.
func TestHostChecksGrantsNameTheirRoom(t *testing.T) {
	api := machineAPI(t)
	api.RoomName = func(roomID string) string {
		if roomID == "room_dyna" {
			return "dyna"
		}
		return roomID
	}
	node, err := api.Store.Save(t.Context(), machines.Machine{Name: "pve", SSH: "pip@pve", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	dyna := authority.WithActor(t.Context(), authority.Actor{Role: authority.Root, Session: "root", Room: "room_dyna"})
	allow, refuse := true, false
	page, err := api.SaveNode(dyna, node.ID, MachinePatch{AllowHostChecks: &allow})
	if err != nil {
		t.Fatal(err)
	}
	if !page.AllowHostChecks || !slices.Equal(page.HostChecksAllowedBy, []string{"root of room dyna"}) {
		t.Fatalf("allowed by = %v, want the room whose root allowed host checks", page.HostChecksAllowedBy)
	}
	stored, err := api.Store.GetByID(t.Context(), node.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !stored.HostChecksAllowed("room_dyna") || stored.HostChecksAllowed("room_other") {
		t.Fatalf("host-check grants = %#v, want the granting room alone", stored.HostChecksGrants)
	}
	// The owner's answer covers every room, and a room root cannot refuse it.
	owner := authority.WithActor(t.Context(), authority.Actor{Role: authority.Owner})
	if page, err = api.SaveNode(owner, node.ID, MachinePatch{AllowHostChecks: &allow}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(page.HostChecksAllowedBy, []string{"the owner"}) {
		t.Fatalf("allowed by = %v, want the owner alone", page.HostChecksAllowedBy)
	}
	if _, err := api.SaveNode(dyna, node.ID, MachinePatch{AllowHostChecks: &refuse}); err == nil || !strings.Contains(err.Error(), "ask the owner") {
		t.Fatalf("a room root refusing the owner's grant = %v, want a refusal naming the owner", err)
	}
	if page, err = api.SaveNode(owner, node.ID, MachinePatch{AllowHostChecks: &refuse}); err != nil {
		t.Fatal(err)
	}
	if page.AllowHostChecks || len(page.HostChecksAllowedBy) != 0 {
		t.Fatalf("allowed by after the owner refused = %v, want none", page.HostChecksAllowedBy)
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
func TestProbeNodeStoresWhatItRead(t *testing.T) {
	api := machineAPI(t)
	remote, err := api.Store.Save(t.Context(), machines.Machine{Name: "hermes", SSH: "pip@hermes", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	prints := func(_ context.Context, _ string, _ io.Reader, out, _ io.Writer, _ string, _ ...string) error {
		_, err := io.WriteString(out, probeOutput)
		return err
	}
	api.Prober = &machines.Prober{Store: api.Store, Command: prints}

	got, err := api.ProbeNode(t.Context(), remote.ID)
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
	down, err := api.ProbeNode(t.Context(), remote.ID)
	if err != nil {
		t.Fatal(err)
	}
	if down.Status != MachineOffline {
		t.Fatalf("status after a failed probe = %s", down.Status)
	}
	if down.Capabilities == nil || down.Capabilities.CPUs != 20 || down.SeenAt == nil {
		t.Fatalf("failed probe lost the reading: %+v", down)
	}
	list, err := api.Nodes(t.Context())
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
func TestProbeNodeCallHasRoomForSSH(t *testing.T) {
	probeCallLeft(t, 0, 19*time.Second)
}

// A machine with a 60 s connect timeout gets a probe budget longer than 60 s
// through the bound call, so a healthy but slow connection is not cancelled
// (D-195, D-197).
func TestProbeNodeCallHonorsConnectTimeout(t *testing.T) {
	probeCallLeft(t, 60*time.Second, 69*time.Second)
}

// probeCallLeft probes a machine through the bound call and checks that the
// probe command ran with at least min left of its deadline.
func probeCallLeft(t *testing.T, connect, min time.Duration) {
	t.Helper()
	api := machineAPI(t)
	remote, err := api.Store.Save(t.Context(), machines.Machine{Name: "hermes", SSH: "pip@hermes", Enabled: true, SSHConnectTimeout: connect})
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
	calls, err := Bind([]Binding{{Name: "ProbeNode", Access: Write, Scope: ScopeAdmin, Method: api.ProbeNode}})
	if err != nil {
		t.Fatal(err)
	}
	id, err := json.Marshal(remote.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := calls["ProbeNode"](t.Context(), []json.RawMessage{id}); err != nil {
		t.Fatal(err)
	}
	// The probe takes its 20 s budget from the call's deadline, so a shorter
	// call deadline clamps it. That is what made a slow SSH connection reach
	// the request deadline and store nothing.
	if left < min {
		t.Fatalf("the probe ran with %v left, so the call deadline clamped its budget below %v", left, min)
	}
	if d := callspec.Deadline("ProbeNode"); d < machines.MaxSSHConnectTimeout+30*time.Second {
		t.Fatalf("ProbeNode has a %v deadline, which cannot cover the longest probe and its store", d)
	}
	page, err := api.Node(t.Context(), remote.ID)
	if err != nil {
		t.Fatal(err)
	}
	if page.Status != MachineOnline || page.SeenAt == nil {
		t.Fatalf("page after the bound probe = %+v", page)
	}
}

// A machine's summary separates owner labels from labels of its last probe.
func TestMachineSummaryCarriesRoomsAndLabels(t *testing.T) {
	got := machineSummary(machines.Machine{
		ID: "m_1", Name: "ci-box", Rooms: []string{"room_a"}, Labels: []string{"ci"},
		Capabilities: machines.Capabilities{OS: "Linux", Arch: "x86_64"},
	})
	if len(got.Rooms) != 1 || got.Rooms[0] != "room_a" || len(got.Labels) != 1 || got.Labels[0] != "ci" {
		t.Fatalf("summary = %+v, want the room and owner label", got)
	}
	wantProbed := []string{"os=linux", "arch=x86_64"}
	if !slices.Equal(got.ProbedLabels, wantProbed) {
		t.Fatalf("probed labels = %v, want %v", got.ProbedLabels, wantProbed)
	}
}
