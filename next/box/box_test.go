package box_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/box/fakehost"
	"github.com/resident-labs/dyna/contract"
)

func TestTransitions(t *testing.T) {
	states := []contract.BoxState{contract.BoxPending, contract.BoxCreating, contract.BoxStarting, contract.BoxReady, contract.BoxStopped, contract.BoxClosing, contract.BoxDeleted, contract.BoxFailed, contract.BoxLost}
	allowed := map[contract.BoxState][]contract.BoxState{
		contract.BoxPending:  {contract.BoxPending, contract.BoxCreating, contract.BoxStarting, contract.BoxClosing},
		contract.BoxCreating: {contract.BoxPending, contract.BoxStarting, contract.BoxReady, contract.BoxFailed, contract.BoxClosing},
		contract.BoxStarting: {contract.BoxReady, contract.BoxStopped, contract.BoxFailed, contract.BoxClosing},
		contract.BoxReady:    {contract.BoxStopped, contract.BoxFailed, contract.BoxClosing, contract.BoxLost},
		contract.BoxStopped:  {contract.BoxStarting, contract.BoxPending, contract.BoxClosing, contract.BoxLost},
		contract.BoxClosing:  {contract.BoxDeleted, contract.BoxLost, contract.BoxFailed},
		contract.BoxFailed:   {contract.BoxClosing, contract.BoxPending},
		contract.BoxLost:     {contract.BoxReady, contract.BoxStopped, contract.BoxFailed, contract.BoxClosing},
	}
	for _, from := range states {
		for _, to := range states {
			want := false
			for _, a := range allowed[from] {
				if a == to {
					want = true
				}
			}
			b, e, err := box.Transition(box.Box{ID: "box_a", State: from}, to, "reason", time.Time{})
			if want {
				if err != nil || b.State != to || e.From != from || e.To != to || e.Box != "box_a" || e.EventType() != "box.changed" {
					t.Fatalf("%s -> %s: %+v %+v %v", from, to, b, e, err)
				}
			} else {
				var te box.TransitionError
				if !errors.As(err, &te) || b.State != from {
					t.Fatalf("%s -> %s accepted", from, to)
				}
			}
		}
	}
}
func TestAdmission(t *testing.T) {
	size, _ := box.Size(contract.BoxSizeSmall)
	h := box.Host{Name: "host", State: box.HostReady, CPUs: 2, MemBytes: 4 * box.GiB, DiskBytes: 40 * box.GiB}
	req := box.Box{ID: "box_a", Size: size}
	if p := box.Admit([]box.Host{h}, nil, req, 20*box.GiB); p.Host != "host" {
		t.Fatal(p)
	}
	occupied := box.Box{ID: "box_b", Host: "host", Size: size, State: contract.BoxReady}
	if p := box.Admit([]box.Host{h}, []box.Box{occupied}, req, 20*box.GiB); p.Reason != box.PendingCPU {
		t.Fatal(p)
	}
}
func TestAdmissionChargesPlacementRoots(t *testing.T) {
	size, _ := box.Size(contract.BoxSizeMedium)
	h := box.Host{Name: "host", State: box.HostReady, CPUs: 4, MemBytes: 8 * box.GiB, DiskBytes: 100 * box.GiB}
	lead := box.Box{ID: "lead", Host: h.Name, Size: size, State: contract.BoxReady}
	child := box.Box{ID: "child", BudgetParent: lead.ID, Size: size}
	if p := box.Admit([]box.Host{h}, []box.Box{lead}, child, 0); p.Host != h.Name {
		t.Fatalf("child: %+v", p)
	}
	if p := box.Admit([]box.Host{h}, []box.Box{lead}, box.Box{ID: "new-lead", Size: size}, 0); p.Reason != box.PendingCPU {
		t.Fatalf("full host: %+v", p)
	}
	h.CPUs, h.MemBytes = 8, 16*box.GiB
	child.Host, child.State = h.Name, contract.BoxReady
	if p := box.Admit([]box.Host{h}, []box.Box{lead, child}, box.Box{ID: "new-lead", Size: size}, 0); p.Host != h.Name {
		t.Fatalf("child charged host: %+v", p)
	}
}
func TestStoppedCountsDiskNotCPUOrMemory(t *testing.T) {
	now := time.Unix(100, 0)
	size, _ := box.Size(contract.BoxSizeSmall)
	h := box.Host{Name: "host", State: box.HostReady, CPUs: 2, MemBytes: 4 * box.GiB, DiskBytes: 40 * box.GiB, CheckedAt: now}
	stopped := box.Box{ID: "box_b", Host: "host", Size: size, State: contract.BoxStopped, PlacedAt: now.Add(time.Second)}
	req := box.Box{ID: "box_a", Size: size}
	if p := box.Admit([]box.Host{h}, []box.Box{stopped}, req, 20*box.GiB); p.Host != "host" {
		t.Fatal(p)
	}
	if p := box.Admit([]box.Host{h}, []box.Box{stopped}, req, 21*box.GiB); p.Reason != box.PendingDisk {
		t.Fatal(p)
	}
}
func TestWakeStaysOnHostAndAccountsDisk(t *testing.T) {
	now := time.Unix(100, 0)
	size, _ := box.Size(contract.BoxSizeSmall)
	hosts := []box.Host{
		{Name: "original", State: box.HostReady, CPUs: 2, MemBytes: 4 * box.GiB, DiskBytes: 20 * box.GiB, CheckedAt: now},
		{Name: "other", State: box.HostReady, CPUs: 8, MemBytes: 32 * box.GiB, DiskBytes: 100 * box.GiB, CheckedAt: now},
	}
	stopped := box.Box{ID: "box_a", Host: "original", Size: size, State: contract.BoxStopped, PlacedAt: now.Add(-time.Second)}
	if p := box.Admit(hosts, []box.Box{stopped}, stopped, 20*box.GiB); p.Host != "original" {
		t.Fatal(p)
	}
	stopped.PlacedAt = now.Add(time.Second)
	if p := box.Admit(hosts, []box.Box{stopped}, stopped, 20*box.GiB); p.Reason != box.PendingDisk {
		t.Fatal(p)
	}
	hosts[0].DiskBytes = 30 * box.GiB
	if p := box.Admit(hosts, []box.Box{stopped}, stopped, 20*box.GiB); p.Host != "original" {
		t.Fatal(p)
	}
}
func TestSliceAndCgroup(t *testing.T) {
	name, err := box.SliceName([]contract.BoxID{"box_a", "box_b", "box_c"})
	if err != nil || name != "dyna-fefe13ab0a05-01052e8504ae-1636b387f6bc.slice" {
		t.Fatalf("%q %v", name, err)
	}
	if got := box.CgroupArgs(name); len(got) != 1 || got[0] != "--cgroup-parent="+name {
		t.Fatal(got)
	}
}
func TestFakeHost(t *testing.T) {
	h := fakehost.New()
	ctx := context.Background()
	if err := h.CreateContainer(ctx, box.ContainerSpec{Box: "box_a"}); err != nil {
		t.Fatal(err)
	}
	if err := h.StartContainer(ctx, "box_a"); err != nil {
		t.Fatal(err)
	}
	got, err := h.ListContainers(ctx)
	if err != nil || len(got) != 1 || !got[0].Running {
		t.Fatal(got, err)
	}
}

func TestAdmissionRespectsHostRooms(t *testing.T) {
	size, _ := box.Size(contract.BoxSizeAgent)
	h := box.Host{Name: "host", State: box.HostReady, CPUs: 2, MemBytes: 4 * box.GiB, DiskBytes: 30 * box.GiB, Rooms: []contract.RoomID{"allowed"}}
	req := box.Box{ID: "box", Room: "other", Size: size}
	if got := box.Admit([]box.Host{h}, nil, req, 0); got.Reason != box.PendingRoomHost {
		t.Fatalf("restricted host: %+v", got)
	}
	req.Room = "allowed"
	if got := box.Admit([]box.Host{h}, nil, req, 0); got.Host != "host" {
		t.Fatalf("allowed room: %+v", got)
	}
	h.Rooms = nil
	req.Room = "other"
	if got := box.Admit([]box.Host{h}, nil, req, 0); got.Host != "host" {
		t.Fatalf("unrestricted host: %+v", got)
	}
}
