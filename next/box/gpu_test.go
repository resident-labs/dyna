package box_test

import (
	"testing"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

func gpuHost(name contract.HostName, cards ...int64) box.Host {
	h := box.Host{Name: name, State: box.HostReady, CPUs: 8, MemBytes: 32 * box.GiB, DiskBytes: 200 * box.GiB, DiskFloor: 20 * box.GiB}
	for i, gib := range cards {
		h.GPUs = append(h.GPUs, contract.GPU{Index: i, Name: "card", MemBytes: gib * box.GiB})
	}
	return h
}

func gpuBox(id contract.BoxID, host contract.HostName, card int, gib contract.GPUGiB, state contract.BoxState) box.Box {
	size, _ := box.Size(contract.BoxSizeAgent)
	return box.Box{ID: id, Room: "room", Host: host, Size: size, State: state, Image: "img", GPU: gib, GPUCard: card}
}

func TestAdmissionSharesACardUntilItsRequestsFill(t *testing.T) {
	// 20 GiB card, a tenth (2 GiB) in reserve: 18 GiB to give.
	hosts := []box.Host{gpuHost("a", 20)}
	size, _ := box.Size(contract.BoxSizeAgent)
	request := func(id contract.BoxID, gib contract.GPUGiB) box.Box {
		return box.Box{ID: id, Room: "room", Size: size, State: contract.BoxPending, GPU: gib}
	}
	var placed []box.Box
	for _, step := range []struct {
		id   contract.BoxID
		gib  contract.GPUGiB
		host contract.HostName
		why  box.PendingReason
	}{
		{"one", 10, "a", ""},
		{"two", 8, "a", ""}, // exactly what is left fits
		{"three", 1, "", box.PendingGPU},
	} {
		got := box.Admit(hosts, placed, request(step.id, step.gib))
		if got.Host != step.host || got.Reason != step.why || got.Card != 0 {
			t.Fatalf("%s: %+v, want host %q reason %q card 0", step.id, got, step.host, step.why)
		}
		if got.Host != "" {
			placed = append(placed, gpuBox(step.id, got.Host, got.Card, step.gib, contract.BoxReady))
		}
	}
}

func TestAdmissionGPUFitIsExact(t *testing.T) {
	hosts := []box.Host{gpuHost("a", 20)}
	size, _ := box.Size(contract.BoxSizeAgent)
	taken := []box.Box{gpuBox("one", "a", 0, 10, contract.BoxReady)}
	for gib, want := range map[contract.GPUGiB]contract.HostName{8: "a", 9: ""} {
		got := box.Admit(hosts, taken, box.Box{ID: "new", Room: "room", Size: size, State: contract.BoxPending, GPU: gib})
		if got.Host != want {
			t.Fatalf("%d GiB beside 10 of 18: host %q, want %q", gib, got.Host, want)
		}
	}
}

func TestAdmissionGPUReserveIsATenthOfTheCard(t *testing.T) {
	hosts := []box.Host{gpuHost("a", 20)}
	size, _ := box.Size(contract.BoxSizeAgent)
	for gib, want := range map[contract.GPUGiB]contract.HostName{18: "a", 19: "", 20: ""} {
		got := box.Admit(hosts, nil, box.Box{ID: "new", Room: "room", Size: size, State: contract.BoxPending, GPU: gib})
		if got.Host != want {
			t.Fatalf("%d GiB on an idle 20 GiB card: host %q, want %q", gib, got.Host, want)
		}
	}
}

func TestAdmissionGPUPicksTheFirstCardThatTakesIt(t *testing.T) {
	hosts := []box.Host{gpuHost("a", 11, 24)}
	size, _ := box.Size(contract.BoxSizeAgent)
	taken := []box.Box{gpuBox("one", "a", 0, 8, contract.BoxReady)}
	for gib, card := range map[contract.GPUGiB]int{1: 0, 2: 1, 20: 1} {
		got := box.Admit(hosts, taken, box.Box{ID: "new", Room: "room", Size: size, State: contract.BoxPending, GPU: gib})
		if got.Host != "a" || got.Card != card {
			t.Fatalf("%d GiB: %+v, want card %d", gib, got, card)
		}
	}
}

func TestAdmissionGPUCountsOnlyBoxesHoldingTheCard(t *testing.T) {
	hosts := []box.Host{gpuHost("a", 20), gpuHost("b", 20)}
	hosts[1].MemUsed = 31 * box.GiB // b is out of memory, so a is the only host a box can take
	size, _ := box.Size(contract.BoxSizeAgent)
	request := box.Box{ID: "new", Room: "room", Size: size, State: contract.BoxPending, GPU: 18}
	for _, tc := range []struct {
		name  string
		taken box.Box
		host  contract.HostName
	}{
		{"running", gpuBox("one", "a", 0, 10, contract.BoxReady), ""},
		{"starting", gpuBox("one", "a", 0, 10, contract.BoxStarting), ""},
		{"stopped", gpuBox("one", "a", 0, 10, contract.BoxStopped), "a"},
		{"pending", gpuBox("one", "a", 0, 10, contract.BoxPending), "a"},
		{"failed", gpuBox("one", "a", 0, 10, contract.BoxFailed), "a"},
		{"deleted", gpuBox("one", "a", 0, 10, contract.BoxDeleted), "a"},
		{"on another host", gpuBox("one", "b", 0, 10, contract.BoxReady), "a"},
		{"without a request", gpuBox("one", "a", 0, 0, contract.BoxReady), "a"},
	} {
		if got := box.Admit(hosts, []box.Box{tc.taken}, request); got.Host != tc.host {
			t.Errorf("%s box on the card: host %q, want %q", tc.name, got.Host, tc.host)
		}
	}
}

func TestAdmissionLeavesBoxesWithoutAGPUAlone(t *testing.T) {
	hosts := []box.Host{gpuHost("a", 20), gpuHost("b")}
	size, _ := box.Size(contract.BoxSizeAgent)
	taken := []box.Box{gpuBox("one", "a", 0, 18, contract.BoxReady)}
	for name, host := range map[string]box.Host{"a": hosts[0], "b": hosts[1]} {
		got := box.Admit([]box.Host{host}, taken, box.Box{ID: "new", Room: "room", Size: size, State: contract.BoxPending})
		if got.Host != host.Name || got.Reason != "" {
			t.Fatalf("a box with no GPU request on %s: %+v", name, got)
		}
	}
}

func TestAdmissionGPUWaitReasons(t *testing.T) {
	size, _ := box.Size(contract.BoxSizeAgent)
	request := box.Box{ID: "new", Room: "room", Size: size, State: contract.BoxPending, GPU: 1}
	if got := box.Admit([]box.Host{gpuHost("a")}, nil, request); got.Host != "" || got.Reason != box.PendingNoGPU {
		t.Fatalf("a host with no card: %+v", got)
	}
	if got := box.Admit([]box.Host{gpuHost("a"), gpuHost("b", 4)}, []box.Box{gpuBox("one", "b", 0, 3, contract.BoxReady)}, request); got.Host != "" || got.Reason != box.PendingGPU {
		t.Fatalf("a taken card beside a host with none: %+v", got)
	}
	if got := box.Admit([]box.Host{gpuHost("a", 0)}, nil, request); got.Host != "" {
		t.Fatalf("a card whose memory is unknown took a request: %+v", got)
	}
}

func TestAdmissionKeepsAPlacedBoxOnItsCard(t *testing.T) {
	hosts := []box.Host{gpuHost("a", 24, 24)}
	stopped := gpuBox("one", "a", 1, 4, contract.BoxStopped)
	got := box.Admit(hosts, []box.Box{stopped}, stopped)
	if got.Host != "a" || got.Card != 1 {
		t.Fatalf("waking a stopped box: %+v, want card 1", got)
	}
	full := []box.Box{stopped, gpuBox("two", "a", 1, 20, contract.BoxReady)}
	if got := box.Admit(hosts, full, stopped); got.Host != "" || got.Reason != box.PendingGPU {
		t.Fatalf("its card is full and the other is free: %+v", got)
	}
}
