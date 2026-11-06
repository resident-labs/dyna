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
		contract.BoxPending:  {contract.BoxFailed, contract.BoxStopped, contract.BoxPending, contract.BoxCreating, contract.BoxStarting, contract.BoxClosing},
		contract.BoxCreating: {contract.BoxStopped, contract.BoxPending, contract.BoxStarting, contract.BoxReady, contract.BoxFailed, contract.BoxClosing},
		contract.BoxStarting: {contract.BoxReady, contract.BoxStopped, contract.BoxFailed, contract.BoxClosing},
		contract.BoxReady:    {contract.BoxStopped, contract.BoxFailed, contract.BoxClosing, contract.BoxLost},
		contract.BoxStopped:  {contract.BoxFailed, contract.BoxCreating, contract.BoxStarting, contract.BoxPending, contract.BoxClosing, contract.BoxLost},
		contract.BoxClosing:  {contract.BoxDeleted, contract.BoxStopped, contract.BoxLost, contract.BoxFailed},
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
func TestAdmissionFollowsARoomsRule(t *testing.T) {
	size, _ := box.Size(contract.BoxSizeSmall)
	host := func(name contract.HostName, pool string, id contract.BoxID) box.Host {
		return box.Host{Name: name, State: box.HostReady, CPUs: 8, MemBytes: 32 * box.GiB, DiskBytes: 200 * box.GiB, DiskFloor: 20 * box.GiB, Pool: pool, Box: id}
	}
	plain, shared, own, theirs := host("plain", "", ""), host("shared", "boats", ""), host("own", "ships", "box_a"), host("theirs", "ships", "box_b")
	request := box.Box{ID: "box_a", Room: "room", Size: size}
	for _, tc := range []struct {
		hosts []box.Host
		only  []string
		want  contract.HostName
	}{
		{[]box.Host{plain, shared}, nil, "plain"},
		{[]box.Host{shared}, nil, "shared"}, // no rule means any host, shared pool nodes included
		{[]box.Host{shared}, []string{"other"}, ""},
		{[]box.Host{own}, nil, "own"},
		{[]box.Host{theirs}, nil, ""},
		{[]box.Host{plain}, []string{"other"}, ""},
		{[]box.Host{plain, shared}, []string{"boats"}, "shared"},
		{[]box.Host{plain, shared}, []string{"plain"}, "plain"},
		{[]box.Host{own}, []string{"ships"}, "own"},
		{[]box.Host{theirs}, []string{"ships"}, ""},
	} {
		p := box.Admit(tc.hosts, nil, request, tc.only...)
		if p.Host != tc.want || (tc.want == "") != (p.Reason == box.PendingRoomHost) {
			t.Errorf("%+v: %+v", tc, p)
		}
	}
	// A shared node lists the rooms that named its pool when it came up, and a room with no rule is served all the same.
	listed := shared
	listed.Rooms = []contract.RoomID{"another"}
	if p := box.Admit([]box.Host{listed}, nil, request); p.Host != "shared" {
		t.Fatalf("no rule, node listing another room: %+v", p)
	}
	// A box already on a host stays there, whatever the rule says now.
	placed := request
	placed.Host, placed.State, placed.Image = "plain", contract.BoxStopped, "image"
	if p := box.Admit([]box.Host{plain}, []box.Box{placed}, placed, "boats"); p.Host != "plain" {
		t.Fatalf("%+v", p)
	}
}

func TestAdmissionHeadroom(t *testing.T) {
	size, _ := box.Size(contract.BoxSizeSmall)
	large := box.Host{Name: "b", State: box.HostReady, CPUs: 8, MemBytes: 128 * box.GiB, DiskBytes: 200 * box.GiB}
	small := box.Host{Name: "a", State: box.HostReady, CPUs: 8, MemBytes: 32 * box.GiB, DiskBytes: 200 * box.GiB}
	busySize, _ := box.Size(contract.BoxSizeXLarge)
	request := box.Box{ID: "new", Size: size}
	for _, tc := range []struct {
		name  string
		hosts []box.Host
		boxes []box.Box
		want  contract.HostName
	}{
		{"CPU busy large host", []box.Host{large, small}, []box.Box{{ID: "busy", Host: large.Name, Size: busySize, State: contract.BoxReady}}, small.Name},
		{"equal headroom chooses smaller name", []box.Host{large, small}, nil, small.Name},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := box.Admit(tc.hosts, tc.boxes, request); got.Host != tc.want {
				t.Fatalf("got %+v, want host %s", got, tc.want)
			}
		})
	}
}

func TestAdmission(t *testing.T) {
	size, _ := box.Size(contract.BoxSizeSmall)
	h := box.Host{Name: "host", State: box.HostReady, CPUs: 1, MemBytes: 4 * box.GiB, DiskBytes: 40 * box.GiB, DiskFloor: 20 * box.GiB}
	req := box.Box{ID: "box_a", Size: size}
	if p := box.Admit([]box.Host{h}, nil, req); p.Host != "host" {
		t.Fatal(p)
	}
	occupiedSize, _ := box.Size(contract.BoxSizeMedium)
	occupied := box.Box{ID: "box_b", Host: "host", Size: occupiedSize, State: contract.BoxReady}
	if p := box.Admit([]box.Host{h}, []box.Box{occupied}, req); p.Reason != box.PendingCPU {
		t.Fatal(p)
	}
}
func TestAdmissionCountsEveryPlacedBoxAtQuarterSize(t *testing.T) {
	size, _ := box.Size(contract.BoxSizeMedium)
	h := box.Host{Name: "host", State: box.HostReady, CPUs: 2, MemBytes: 16 * box.GiB, DiskBytes: 100 * box.GiB}
	for _, state := range []contract.BoxState{contract.BoxCreating, contract.BoxStarting, contract.BoxReady, contract.BoxClosing, contract.BoxLost} {
		placed := []box.Box{{ID: "a", Host: h.Name, Size: size, State: state}}
		request := box.Box{ID: "b", Size: size}
		if p := box.Admit([]box.Host{h}, placed, request); p.Host != h.Name {
			t.Fatalf("%s: %+v", state, p)
		}
		placed = append(placed, box.Box{ID: "b", Host: h.Name, Size: size, State: contract.BoxCreating})
		request.ID = "c"
		if p := box.Admit([]box.Host{h}, placed, request); p.Reason != box.PendingCPU {
			t.Fatalf("%s: %+v", state, p)
		}
	}
}
func TestRunnerAdmissionFitsRealMemory(t *testing.T) {
	size, _ := box.Size(contract.BoxSizeMedium)
	size.MemBytes = 8 * box.GiB
	request := box.Box{ID: "new", Image: box.RunnerImage, Size: size}
	for _, tc := range []struct {
		name   string
		memory int64
		want   box.Placement
	}{
		{"small host refuses second runner", 11 * box.GiB, box.Placement{Reason: box.PendingMemory}},
		{"larger host fits both runners", 16 * box.GiB, box.Placement{Host: "host"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			host := box.Host{Name: "host", State: box.HostReady, CPUs: 7, MemBytes: tc.memory, DiskBytes: 200 * box.GiB}
			placed := box.Box{ID: "old", Host: host.Name, Image: box.RunnerImage, Size: size, State: contract.BoxReady}
			if got := box.Admit([]box.Host{host}, []box.Box{placed}, request); got != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestAdmissionStopsAtRealMemoryThreshold(t *testing.T) {
	size, _ := box.Size(contract.BoxSizeAgent)
	host := box.Host{Name: "host", State: box.HostReady, CPUs: 4, MemBytes: 10 * box.GiB, DiskBytes: 100 * box.GiB}
	request := box.Box{ID: "box_a", Size: size}
	host.MemUsed = 8*box.GiB - 1
	if p := box.Admit([]box.Host{host}, nil, request); p.Host != host.Name {
		t.Fatal(p)
	}
	host.MemUsed++
	if p := box.Admit([]box.Host{host}, nil, request); p.Reason != box.PendingMemory {
		t.Fatal(p)
	}
}
func TestStoppedCountsDiskNotCPUOrMemory(t *testing.T) {
	now := time.Unix(100, 0)
	size, _ := box.Size(contract.BoxSizeSmall)
	h := box.Host{Name: "host", State: box.HostReady, CPUs: 2, MemBytes: 4 * box.GiB, DiskBytes: 40 * box.GiB, DiskFloor: 20 * box.GiB, CheckedAt: now}
	stopped := box.Box{ID: "box_b", Host: "host", Size: size, State: contract.BoxStopped, PlacedAt: now.Add(time.Second)}
	req := box.Box{ID: "box_a", Size: size}
	if p := box.Admit([]box.Host{h}, []box.Box{stopped}, req); p.Host != "host" {
		t.Fatal(p)
	}
	h.DiskFloor = 21 * box.GiB
	if p := box.Admit([]box.Host{h}, []box.Box{stopped}, req); p.Reason != box.PendingDisk {
		t.Fatal(p)
	}
}
func TestWakeStaysOnHostAndAccountsDisk(t *testing.T) {
	now := time.Unix(100, 0)
	size, _ := box.Size(contract.BoxSizeSmall)
	hosts := []box.Host{
		{Name: "original", State: box.HostReady, CPUs: 2, MemBytes: 4 * box.GiB, DiskBytes: 20 * box.GiB, DiskFloor: 20 * box.GiB, CheckedAt: now},
		{Name: "other", State: box.HostReady, CPUs: 8, MemBytes: 32 * box.GiB, DiskBytes: 100 * box.GiB, CheckedAt: now},
	}
	stopped := box.Box{ID: "box_a", Host: "original", Size: size, State: contract.BoxStopped, PlacedAt: now.Add(-time.Second)}
	if p := box.Admit(hosts, []box.Box{stopped}, stopped); p.Host != "original" {
		t.Fatal(p)
	}
	stopped.PlacedAt = now.Add(time.Second)
	if p := box.Admit(hosts, []box.Box{stopped}, stopped); p.Reason != box.PendingDisk {
		t.Fatal(p)
	}
	hosts[0].DiskBytes = 30 * box.GiB
	if p := box.Admit(hosts, []box.Box{stopped}, stopped); p.Host != "original" {
		t.Fatal(p)
	}
}
func TestDiskFloorAndLow(t *testing.T) {
	for _, tc := range []struct{ total, floor int64 }{
		{0, 20 * box.GiB},
		{100 * box.GiB, 20 * box.GiB},
		{146 * box.GiB, 20 * box.GiB},
		{400 * box.GiB, 40 * box.GiB},
		{1000 * box.GiB, 100 * box.GiB},
		{4000 * box.GiB, 400 * box.GiB},
	} {
		floor := box.DiskFloor(tc.total)
		if floor != tc.floor {
			t.Errorf("total %d: floor %d, want %d", tc.total, floor, tc.floor)
		}
		for free, low := range map[int64]bool{floor - 1: true, floor: false, floor + 1: false} {
			if got := box.DiskLow(free, floor); got != low {
				t.Errorf("total %d free %d: low %v, want %v", tc.total, free, got, low)
			}
		}
	}
}
func TestHostAtNinetyPercentRefusesNewBoxesAndWakes(t *testing.T) {
	now := time.Unix(100, 0)
	size, _ := box.Size(contract.BoxSizeAgent)
	floor := box.DiskFloor(1000 * box.GiB)
	h := box.Host{Name: "host", State: box.HostReady, CPUs: 8, MemBytes: 32 * box.GiB, DiskBytes: floor, DiskFloor: floor, CheckedAt: now}
	if p := box.Admit([]box.Host{h}, nil, box.Box{ID: "new", Size: size}); p.Reason != box.PendingDisk {
		t.Fatalf("new box at 90%%: %+v", p)
	}
	stopped := box.Box{ID: "wake", Host: "host", Image: "image", Size: size, State: contract.BoxStopped, PlacedAt: now.Add(-time.Second)}
	if p := box.Admit([]box.Host{h}, []box.Box{stopped}, stopped); p.Host != "host" {
		t.Fatalf("wake exactly at the floor: %+v", p)
	}
	h.DiskBytes--
	if p := box.Admit([]box.Host{h}, []box.Box{stopped}, stopped); p.Reason != box.PendingDisk {
		t.Fatalf("wake below the floor: %+v", p)
	}
}

// Admission still reserves a new box's request even when the host itself is not low.
func TestAdmissionCanWaitAboveDiskFloor(t *testing.T) {
	now := time.Unix(100, 0)
	floor := box.DiskFloor(1000 * box.GiB)
	for _, name := range []contract.BoxSizeName{contract.BoxSizeAgent, contract.BoxSizeSmall, contract.BoxSizeMedium, contract.BoxSizeLarge, contract.BoxSizeXLarge} {
		size, _ := box.Size(name)
		h := box.Host{Name: "host", State: box.HostReady, CPUs: 64, MemBytes: 256 * box.GiB, DiskBytes: floor + 1, DiskFloor: floor, CheckedAt: now}
		if box.DiskLow(h.DiskBytes, floor) {
			t.Fatalf("size %s: unexpectedly low", name)
		}
		if p := box.Admit([]box.Host{h}, nil, box.Box{ID: "new", Size: size}); p.Reason != box.PendingDisk {
			t.Errorf("size %s: %+v, want disk wait above floor", name, p)
		}
	}
}
func TestPinnedPendingReservesDiskAfterHostSample(t *testing.T) {
	now := time.Unix(100, 0)
	size, _ := box.Size(contract.BoxSizeSmall)
	hosts := []box.Host{{Name: "original", State: box.HostReady, CPUs: 2, MemBytes: 4 * box.GiB, DiskBytes: 20 * box.GiB, DiskFloor: 20 * box.GiB, CheckedAt: now}, {Name: "other", State: box.HostReady, CPUs: 8, MemBytes: 32 * box.GiB, DiskBytes: 100 * box.GiB, CheckedAt: now}}
	pending := box.Box{ID: "child", Host: "original", Size: size, State: contract.BoxPending, PlacedAt: now.Add(-time.Second)}
	if p := box.Admit(hosts, []box.Box{pending}, pending); p.Reason != box.PendingDisk {
		t.Fatal(p)
	}
}

func TestSliceAndCgroup(t *testing.T) {
	name, err := box.SliceName("box_a")
	if err != nil || name != "dyna-box_a.slice" {
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
	if got := box.Admit([]box.Host{h}, nil, req); got.Reason != box.PendingRoomHost {
		t.Fatalf("restricted host: %+v", got)
	}
	req.Room = "allowed"
	if got := box.Admit([]box.Host{h}, nil, req); got.Host != "host" {
		t.Fatalf("allowed room: %+v", got)
	}
	h.Rooms = nil
	req.Room = "other"
	if got := box.Admit([]box.Host{h}, nil, req); got.Host != "host" {
		t.Fatalf("unrestricted host: %+v", got)
	}
}

func TestXLargeIsTheSizeTheSpecNames(t *testing.T) {
	size, ok := box.Size(contract.BoxSizeXLarge)
	if !ok || size.CPUs != 16 || size.MemBytes != 32*box.GiB || size.MemBytes*3/2 != 48*box.GiB || size.Pids != 16384 {
		t.Fatalf("%+v", size)
	}
	host := box.Host{Name: "host", State: box.HostReady, CPUs: 64, MemBytes: 256 * box.GiB, DiskBytes: 100 * box.GiB, DiskFloor: 20 * box.GiB, CheckedAt: time.Unix(100, 0)}
	if p := box.Admit([]box.Host{host}, nil, box.Box{ID: "new", Size: size}); p.Host != "host" {
		t.Fatalf("100 GiB free fits an 80 GiB request above a 20 GiB floor: %+v", p)
	}
	host.DiskBytes = 99 * box.GiB
	if p := box.Admit([]box.Host{host}, nil, box.Box{ID: "new", Size: size}); p.Reason != box.PendingDisk {
		t.Fatalf("99 GiB free: %+v", p)
	}
}
