package jobs

import (
	"strings"
	"testing"

	"dyna-agent/internal/machines"
)

func queuedContainerService(t *testing.T, machine machines.Machine) *Service {
	t.Helper()
	dir := t.TempDir()
	return &Service{
		Store: Store{DataDir: dir}, DataDir: dir,
		Machines: fakeMachines{local: machine, byName: map[string]machines.Machine{machine.Name: machine}},
	}
}

func containerJobScript(job Job) string {
	return jobScript(machines.Machine{Name: job.Node}, job, Resources{CPUs: 1, MemoryMiB: 128}, "")
}

func TestContainerNetworkNone(t *testing.T) {
	svc := queuedContainerService(t, machines.Machine{Name: "local", Enabled: true})
	job, err := svc.Submit(t.Context(), Input{RoomID: "room_a", Command: []string{"true"}, Image: "alpine", Network: "none"})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := svc.Store.Get(t.Context(), job.ID)
	if err != nil || stored.Container.Network != "none" {
		t.Fatalf("stored container options = %#v, %v", stored.Container, err)
	}
	if script := containerJobScript(stored); !strings.Contains(script, " --network=none") {
		t.Fatalf("container command has no isolated network:\n%s", script)
	}
	if _, err := svc.Submit(t.Context(), Input{Command: []string{"true"}, Image: "alpine", Network: "bridge"}); err == nil {
		t.Fatal("an unsupported network was accepted")
	}
}

func TestContainerNamedMounts(t *testing.T) {
	machine := machines.Machine{Name: "local", Enabled: true, Mounts: []machines.NamedMount{
		{Name: "data", Source: "/srv/private/data", Room: "room_a", Mode: machines.MountRO},
		{Name: "cache", Source: "/var/cache/dyna/room_a", Room: "room_a", Mode: machines.MountRW},
		{Name: "other", Source: "/srv/other", Room: "room_b", Mode: machines.MountRO},
	}}
	svc := queuedContainerService(t, machine)
	job, err := svc.Submit(t.Context(), Input{RoomID: "room_a", Command: []string{"true"}, Image: "alpine", Mounts: []MountRequest{
		{Name: "data", Target: "/data"},
		{Name: "cache", Target: "/cache", Mode: machines.MountRO},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(job.Container.Mounts) != 2 || job.Container.Mounts[0].Source != "/srv/private/data" || job.Container.Mounts[1].Mode != machines.MountRO {
		t.Fatalf("resolved mounts = %#v", job.Container.Mounts)
	}
	script := containerJobScript(job)
	for _, want := range []string{"-v '/srv/private/data:/data:ro'", "-v '/var/cache/dyna/room_a:/cache:ro'", "-w /workspace"} {
		if !strings.Contains(script, want) {
			t.Errorf("container command lacks %q:\n%s", want, script)
		}
	}
	for _, test := range []struct {
		name  string
		mount MountRequest
		want  string
	}{
		{name: "unknown", mount: MountRequest{Name: "missing", Target: "/data"}, want: "unknown"},
		{name: "another room", mount: MountRequest{Name: "other", Target: "/data"}, want: "room"},
		{name: "write to read only", mount: MountRequest{Name: "data", Target: "/data", Mode: machines.MountRW}, want: "read-only"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := svc.Submit(t.Context(), Input{RoomID: "room_a", Command: []string{"true"}, Image: "alpine", Mounts: []MountRequest{test.mount}})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("mount refusal = %v, want text %q", err, test.want)
			}
		})
	}
}

func TestContainerAllowedDevice(t *testing.T) {
	machine := machines.Machine{Name: "local", Enabled: true, Devices: []machines.DeviceGrant{{Path: "/dev/dri", Room: "room_a"}}}
	svc := queuedContainerService(t, machine)
	job, err := svc.Submit(t.Context(), Input{RoomID: "room_a", Command: []string{"true"}, Image: "alpine", Devices: []string{"/dev/dri"}})
	if err != nil {
		t.Fatal(err)
	}
	if script := containerJobScript(job); !strings.Contains(script, " --device='/dev/dri'") {
		t.Fatalf("container command has no allowed render device:\n%s", script)
	}
	if _, err := svc.Submit(t.Context(), Input{RoomID: "room_a", Command: []string{"true"}, Image: "alpine", Devices: []string{"/dev/kvm"}}); err == nil {
		t.Fatal("a device outside the node allow list was accepted")
	}
	if _, err := svc.Submit(t.Context(), Input{RoomID: "room_b", Command: []string{"true"}, Image: "alpine", Devices: []string{"/dev/dri"}}); err == nil {
		t.Fatal("a device registered for another room was accepted")
	}
}
