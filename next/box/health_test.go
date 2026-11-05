package box_test

import (
	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"testing"
)

func TestDegradedHostRefusesNewAllocationsNotWakes(t *testing.T) {
	size, _ := box.Size(contract.BoxSizeAgent)
	host := box.Host{Name: "arch", State: box.HostReady, Health: contract.HostDegraded, CPUs: 8, MemBytes: 32 * box.GiB, DiskBytes: 200 * box.GiB, DiskFloor: 20 * box.GiB, CI: true}
	request := box.Box{ID: "b", Room: "r", Size: size, State: contract.BoxPending}
	if p := box.Admit([]box.Host{host}, nil, request); p.Host != "" || p.Reason != box.PendingNoReadyHost {
		t.Fatalf("new box admitted: %+v", p)
	}
	if len(box.RunnerHosts([]box.Host{host})) != 0 {
		t.Fatal("degraded host offered to CI")
	}
	request.Host, request.Image, request.State = "arch", "image", contract.BoxStopped
	if p := box.Admit([]box.Host{host}, []box.Box{request}, request); p.Host != "arch" {
		t.Fatalf("existing box blocked: %+v", p)
	}
	request.Host, request.Image, request.State = "", "", contract.BoxPending
	for _, health := range []contract.HostHealth{contract.HostHealthUnknown, contract.HostHealthy} {
		host.Health = health
		if p := box.Admit([]box.Host{host}, nil, request); p.Host != "arch" {
			t.Fatalf("%s host blocked: %+v", health, p)
		}
	}
}
