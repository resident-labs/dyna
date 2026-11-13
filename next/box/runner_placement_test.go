package box_test

import (
	"fmt"
	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"slices"
	"testing"
)

func TestRunnerPlacement(t *testing.T) {
	size, _ := box.Size(contract.BoxSizeSmall)
	request := box.Box{ID: "new", Image: box.RunnerImage, Size: size}
	for _, tt := range []struct {
		name       string
		big, small *float64
		readySmall bool
		want       contract.HostName
	}{
		{"saturated big", ptr(48), ptr(0), false, "small"},
		{"missing heartbeat", nil, nil, false, "big"},
		{"image ready preference", ptr(0), ptr(0), true, "small"},
		{"all saturated", ptr(48), ptr(8), false, "big"},
		{"idle cores ranking", ptr(47), ptr(0), false, "small"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			hosts := []box.Host{
				{Name: "big", State: box.HostReady, CPUs: 48, CPUUsed: tt.big, MemBytes: 128 * box.GiB, DiskBytes: 1000 * box.GiB, CIScale: 1},
				{Name: "small", State: box.HostReady, CPUs: 8, CPUUsed: tt.small, MemBytes: 32 * box.GiB, DiskBytes: 1000 * box.GiB, CIScale: 1},
			}
			ready := hosts[:1]
			if tt.readySmall {
				ready = hosts[1:]
			}
			if p := box.AdmitRunner(hosts, ready, nil, request); p.Host != tt.want {
				t.Fatalf("got %+v, want %s", p, tt.want)
			}
			if tt.name == "saturated big" || tt.name == "idle cores ranking" {
				if p := box.Admit(hosts, nil, request); p.Host != "small" {
					t.Fatalf("real idle cores ranking: %+v", p)
				}
				if p := box.AdmitRunner(hosts, hosts, nil, request); p.Host != "small" {
					t.Fatalf("ready hosts ranking: %+v", p)
				}
			}
			session := request
			session.Image = "session"
			if p := box.Admit(hosts, nil, session); p.Host != "big" {
				t.Fatalf("session placement changed: %+v", p)
			}
		})
	}
}

func ptr(v float64) *float64 { return &v }

func TestRunnerBurstPlacement(t *testing.T) {
	size, _ := box.Size(contract.BoxSizeLarge)
	var hosts []box.Host
	for _, cores := range []int{48, 27, 20, 19, 15} {
		hosts = append(hosts, box.Host{Name: contract.HostName(fmt.Sprint(cores)), State: box.HostReady, CPUs: cores, CPUUsed: ptr(1), MemBytes: 256 * box.GiB, DiskBytes: 10000 * box.GiB, CIScale: 4})
	}
	var boxes []box.Box
	counts := make(map[contract.HostName]int)
	for i := 0; i < 10; i++ {
		request := box.Box{ID: contract.BoxID(fmt.Sprint(i)), Image: box.RunnerImage, Size: size, State: contract.BoxReady}
		p := box.AdmitRunner(hosts, hosts, boxes, request)
		if p.Host == "" {
			t.Fatalf("runner %d: %+v", i, p)
		}
		request.Host = p.Host
		boxes = append(boxes, request)
		counts[p.Host]++
	}
	// Ten runners at half their CPUs each are 40 busy cores on a fleet of 129: every host but the smallest takes some, and none
	// is filled past its cores while others idle.
	if len(counts) < len(hosts)-1 || counts["48"] > 6 {
		t.Fatalf("burst did not spread across fleet: %v", counts)
	}
	for _, h := range hosts {
		if counts[h.Name]*size.CPUs/2 > h.CPUs {
			t.Fatalf("host %s overfilled while fleet had idle cores: %v", h.Name, counts)
		}
	}
}

// A box that lives on a host wakes there while CI runners hold reservations: hermeslab, 2026-11-13, 26 GiB and 19 CPUs for
// boxes, three large boxes and two root boxes running, three runners reserving 40 GiB.
func TestRunnersDoNotHoldOutBoxOnItsHost(t *testing.T) {
	large, _ := box.Size(contract.BoxSizeLarge)
	medium, _ := box.Size(contract.BoxSizeMedium)
	agent, _ := box.Size(contract.BoxSizeAgent)
	host := box.Host{Name: "host", State: box.HostReady, CPUs: 19, CIScale: 2, MemBytes: 26 * box.GiB, DiskBytes: 1000 * box.GiB}
	placed := []box.Box{{ID: "waking", Host: host.Name, Image: "session", Size: large, State: contract.BoxPending}}
	for i, size := range []contract.BoxSize{large, large, large, agent, agent} {
		placed = append(placed, box.Box{ID: contract.BoxID(fmt.Sprint("session-", i)), Host: host.Name, Image: "session", Size: size, State: contract.BoxReady})
	}
	sessionsOnly := len(placed)
	for i, size := range []contract.BoxSize{large, medium, large} {
		placed = append(placed, box.Box{ID: contract.BoxID(fmt.Sprint("runner-", i)), Host: host.Name, Image: box.RunnerImage, Size: size, State: contract.BoxReady})
	}
	if p := box.Admit([]box.Host{host}, placed, placed[0]); p.Host != host.Name {
		t.Fatalf("session box behind runners: %+v", p)
	}
	// A box that has a host but no container yet (a create that failed and retries) holds nothing there, so it waits too.
	retry := slices.Clone(placed)
	retry[0].Image = ""
	if p := box.Admit([]box.Host{host}, retry, retry[0]); p.Reason != box.PendingMemory {
		t.Fatalf("box with no container beside runners: %+v", p)
	}
	// A new box can go elsewhere, so it still waits where runners fill the ceiling.
	if p := box.Admit([]box.Host{host}, placed, box.Box{ID: "new", Image: "session", Size: large}); p.Reason != box.PendingMemory {
		t.Fatalf("new box beside runners: %+v", p)
	}
	// The runner's own check still counts every reservation: with the box awake, a fourth runner does not fit.
	placed[0].State = contract.BoxReady
	runner := box.Box{ID: "runner-new", Image: box.RunnerImage, Size: large}
	if p := box.Admit([]box.Host{host}, placed, runner); p.Reason != box.PendingMemory {
		t.Fatalf("runner past the shared ceiling: %+v", p)
	}
	// Session boxes still stop at four times the host among themselves.
	sessions := placed[:sessionsOnly]
	for i := 0; i < 3; i++ {
		sessions = append(sessions, box.Box{ID: contract.BoxID(fmt.Sprint("more-", i)), Host: host.Name, Image: "session", Size: large, State: contract.BoxReady})
	}
	if p := box.Admit([]box.Host{host}, sessions, box.Box{ID: "new", Image: "session", Size: large}); p.Reason != box.PendingMemory {
		t.Fatalf("session box past four times the host: %+v", p)
	}
}
