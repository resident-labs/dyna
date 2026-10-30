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
		contract.BoxPending:  {contract.BoxCreating, contract.BoxStarting},
		contract.BoxCreating: {contract.BoxStarting, contract.BoxFailed},
		contract.BoxStarting: {contract.BoxReady, contract.BoxFailed},
		contract.BoxReady:    {contract.BoxStopped, contract.BoxClosing, contract.BoxLost},
		contract.BoxStopped:  {contract.BoxStarting, contract.BoxPending, contract.BoxClosing, contract.BoxLost},
		contract.BoxClosing:  {contract.BoxDeleted, contract.BoxLost, contract.BoxFailed},
		contract.BoxLost:     {contract.BoxReady, contract.BoxFailed, contract.BoxClosing},
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
