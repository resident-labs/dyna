package box

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

// ovh-rise5 as the design assumes it: 48 threads less one, and the memory total less the 4 GiB floor.
func ovh() Host {
	engine := false
	return Host{Name: "ovh-rise5", State: HostReady, EngineHost: &engine, CI: true, CPUs: 47, MemBytes: 1217 * GiB / 10, DiskBytes: 800 * GiB, DiskFloor: 89 * GiB, CheckedAt: time.Unix(1, 0)}
}

var largeRunner = contract.BoxSize{Name: contract.BoxSizeLarge, CPUs: 8, MemBytes: 16 * GiB}

func relay(t *testing.T, cpus float64, memory contract.ServiceSize) ServiceRequest {
	t.Helper()
	parts, err := contract.ServiceSpec{Name: "relay", Image: "friends-relay", CPUs: cpus, Memory: memory}.Parse()
	if err != nil {
		t.Fatal(err)
	}
	return ServiceRequest{ID: "svc_relay", Room: "horizon", Parts: parts, ImageEstimate: GiB}
}

// runners counts how many large runners admission takes at once on the host.
func runners(h Host) int {
	var placed []Box
	for i := 0; ; i++ {
		request := Box{ID: contract.BoxID(fmt.Sprintf("box_r%d", i)), Room: "horizon", Image: RunnerImage, Size: largeRunner, State: contract.BoxPending}
		p := Admit([]Host{h}, placed, request)
		if p.Host == "" {
			return len(placed)
		}
		request.Host, request.State, request.PlacedAt = p.Host, contract.BoxReady, time.Unix(2, 0)
		placed = append(placed, request)
	}
}

// The design's worked example (5.18, ovh-rise5): the relay takes one CPU and 512 MiB and costs CI no runner slot, because
// memory binds the runners at 7 and 7 x 16 GiB still fits.
func TestServiceCostsNoRunnerSlotOnOvh(t *testing.T) {
	h := ovh()
	before := runners(h)
	h.Services = []ServiceHold{{ID: "svc_relay", Room: "horizon", CPUMilli: 1000, MemBytes: 512 << 20, DiskBytes: 2 * GiB}}
	after := runners(h)
	if before != 7 || after != 7 {
		t.Fatalf("large runners before %d, after %d, want 7 and 7", before, after)
	}
}

// A service counts at 1x and comes off the top, so the box CPU budget of 4 x 47 drops by 4 for each service CPU.
func TestServicesComeOffTheTopOfBoxBudgets(t *testing.T) {
	h := ovh()
	h.Services = []ServiceHold{{ID: "svc_a", Room: "x", CPUMilli: 10000, MemBytes: GiB}}
	small := contract.BoxSize{Name: contract.BoxSizeSmall, CPUs: 4, MemBytes: GiB}
	var boxes []Box
	for i := 0; ; i++ {
		request := Box{ID: contract.BoxID(fmt.Sprintf("box_%d", i)), Room: "r", Size: small, State: contract.BoxPending}
		p := Admit([]Host{h}, boxes, request)
		if p.Host == "" {
			if p.Reason != PendingCPU {
				t.Fatalf("reason %q, want cpu", p.Reason)
			}
			break
		}
		request.Host, request.State, request.PlacedAt = p.Host, contract.BoxReady, time.Unix(2, 0)
		boxes = append(boxes, request)
	}
	// 4 x (47 - 10) = 148 CPUs of boxes, 4 each.
	if len(boxes) != 37 {
		t.Fatalf("%d boxes of 4 CPUs, want 37", len(boxes))
	}
}

func TestServiceCapIsAQuarterOfTheHost(t *testing.T) {
	h := ovh()
	// 47/4 = 11.75 CPUs and 121.7/4 = 30.4 GiB.
	for _, c := range []struct {
		name   string
		held   int64
		cpus   float64
		memory contract.ServiceSize
		want   Placement
	}{
		{"11.75 CPUs fit alone", 0, 8, "512MiB", Placement{Host: "ovh-rise5"}},
		{"3.5 on top of 8 fits", 8000, 3.5, "512MiB", Placement{Host: "ovh-rise5"}},
		{"3.75 on top of 8 is exactly the cap", 8000, 3.75, "512MiB", Placement{Host: "ovh-rise5"}},
		{"4 on top of 8 is past the cap", 8000, 4, "512MiB", Placement{Reason: PendingCPU}},
		{"memory past a quarter", 0, 1, "16GiB", Placement{Host: "ovh-rise5"}},
	} {
		h.Services = nil
		if c.held > 0 {
			h.Services = []ServiceHold{{ID: "svc_held", Room: "x", CPUMilli: c.held, MemBytes: GiB, DiskBytes: GiB}}
		}
		if got := AdmitService([]Host{h}, nil, relay(t, c.cpus, c.memory)); got != c.want {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}
	h.Services = []ServiceHold{{ID: "svc_held", Room: "x", CPUMilli: 1000, MemBytes: 16 * GiB, DiskBytes: GiB}}
	if got := AdmitService([]Host{h}, nil, relay(t, 1, "16GiB")); got.Reason != PendingMemory {
		t.Errorf("16 + 16 GiB is past the quarter of 121.7: %+v", got)
	}
}

// A service waits with cpu when the load already counted would not fit after it, and boxes are never stopped to make room.
func TestServiceWaitsForCPUWhenExistingLoadDoesNotFit(t *testing.T) {
	h := ovh()
	// 47 CPUs of boxes sit at 4 x 47 = 188; one service CPU leaves 4 x 46 = 184, which the 188 no longer fits.
	boxes := []Box{{ID: "box_big", Room: "r", Host: h.Name, Image: "x", State: contract.BoxReady, Size: contract.BoxSize{Name: contract.BoxSizeSmall, CPUs: 188, MemBytes: GiB}, PlacedAt: time.Unix(2, 0)}}
	got := AdmitService([]Host{h}, boxes, relay(t, 1, "512MiB"))
	if got.Host != "" || got.Reason != PendingCPU {
		t.Fatalf("%+v, want pending cpu", got)
	}
	boxes[0].Size.CPUs = 184
	if got := AdmitService([]Host{h}, boxes, relay(t, 1, "512MiB")); got.Host != "ovh-rise5" {
		t.Fatalf("184 CPUs of boxes fit after the cut: %+v", got)
	}
}

func TestServiceNeverGoesOnTheEngineHostAnExecOnlyHostOrANode(t *testing.T) {
	engine := true
	for name, mutate := range map[string]func(*Host){
		"engine host":    func(h *Host) { h.EngineHost = &engine },
		"exec only host": func(h *Host) { h.ExecOnly = true },
		"pool node":      func(h *Host) { h.Pool = "boats" },
		"unready":        func(h *Host) { h.State = HostUnreachable },
		"not the room":   func(h *Host) { h.Rooms = []contract.RoomID{"other"} },
	} {
		h := ovh()
		mutate(&h)
		if got := AdmitService([]Host{h}, nil, relay(t, 1, "512MiB")); got.Host != "" {
			t.Errorf("%s: placed on %s", name, got.Host)
		}
	}
	h := ovh()
	h.NoAgents, h.CI = true, false
	if got := AdmitService([]Host{h}, nil, relay(t, 1, "512MiB")); got.Host != "ovh-rise5" {
		t.Errorf("a service needs no agents or ci switch: %+v", got)
	}
}

func TestServiceHonoursTheRoomRuleAndDisk(t *testing.T) {
	h := ovh()
	if got := AdmitService([]Host{h}, nil, relay(t, 1, "512MiB"), "elsewhere"); got.Host != "" || got.Reason != PendingRoomHost {
		t.Errorf("room rule: %+v", got)
	}
	h.DiskBytes = h.DiskFloor + 2*GiB // 1 GiB disk plus the 1 GiB image estimate leaves exactly the floor
	if got := AdmitService([]Host{h}, nil, relay(t, 1, "512MiB")); got.Host != "ovh-rise5" {
		t.Errorf("exactly the floor: %+v", got)
	}
	h.DiskBytes--
	if got := AdmitService([]Host{h}, nil, relay(t, 1, "512MiB")); got.Reason != PendingDisk {
		t.Errorf("a byte under the floor: %+v", got)
	}
}

func TestServicesCountAtOneTimesAgainstRoomLimits(t *testing.T) {
	boxes := []Box{{ID: "box_a", Room: "r", Host: "h", State: contract.BoxReady, Size: contract.BoxSize{CPUs: 8, MemBytes: 8 * GiB}}}
	holds := []ServiceHold{{ID: "svc_a", Room: "r", CPUMilli: 2000, MemBytes: GiB}, {ID: "svc_b", Room: "other", CPUMilli: 9000, MemBytes: GiB}}
	cpu, mem := RoomQuarters("r", boxes, holds, "", "")
	if cpu != 8000+4*2000 || mem != 8*GiB+4*GiB {
		t.Fatalf("cpu %d mem %d", cpu, mem)
	}
	if cpu, _ := RoomQuarters("r", boxes, holds, "", "svc_a"); cpu != 8000 {
		t.Fatalf("skipping the service itself: %d", cpu)
	}
}

// The memory budget drops with a service's memory as the CPU budget does: 4 x (121.7 - 30) GiB of boxes, not 4 x 121.7.
func TestServicesComeOffTheTopOfBoxMemory(t *testing.T) {
	h := ovh()
	h.Services = []ServiceHold{{ID: "svc_a", Room: "x", CPUMilli: 1000, MemBytes: 30 * GiB}}
	request := Box{ID: "box_big", Room: "r", Size: contract.BoxSize{Name: contract.BoxSizeSmall, CPUs: 1, MemBytes: 400 * GiB}, State: contract.BoxPending}
	if p := Admit([]Host{h}, nil, request); p.Host != "" || p.Reason != PendingMemory {
		t.Fatalf("placement %+v, want memory pending", p)
	}
	h.Services = nil
	if p := Admit([]Host{h}, nil, request); p.Host != h.Name {
		t.Fatalf("without the service %+v, want a placement", p)
	}
}

// ResetEngineHosts clears the engine-host flag at every engine start. A host whose flag is unknown may be the engine's own.
func TestServiceNeverGoesOnAHostWhoseEngineFlagIsUnknown(t *testing.T) {
	h := ovh()
	h.EngineHost = nil
	request := ServiceRequest{ID: "svc_a", Room: "r", Parts: oneCPUService(t)}
	if p := AdmitService([]Host{h}, nil, request); p.Host != "" {
		t.Fatalf("placed on %s with the engine flag unknown", p.Host)
	}
}

// A declared host narrows the room's rule and never replaces it.
func TestServiceHostIsInsideTheRoomRule(t *testing.T) {
	a, b := ovh(), ovh()
	a.Name, b.Name = "a", "b"
	request := ServiceRequest{ID: "svc_a", Room: "r", Parts: oneCPUService(t)}
	request.Parts.Spec.Host = "b"
	if p := AdmitService([]Host{a, b}, nil, request); p.Host != "b" {
		t.Fatalf("placement %+v, want b", p)
	}
	if p := AdmitService([]Host{a, b}, nil, request, "a"); p.Host != "" || p.Reason != PendingRoomHost {
		t.Fatalf("placement %+v, want room_host pending for a host outside the rule", p)
	}
}

func oneCPUService(t *testing.T) contract.ServiceParts {
	t.Helper()
	parts, err := contract.ServiceSpec{Name: "web", Image: "ghcr.io/o/r@sha256:" + strings.Repeat("a", 64), CPUs: 1, Memory: "512MiB"}.Parse()
	if err != nil {
		t.Fatal(err)
	}
	return parts
}

func TestServiceRoomBudgetCountsBoxesAtAQuarterAndServicesWhole(t *testing.T) {
	request := ServiceRequest{ID: "svc_self", Room: "r", Parts: oneCPUService(t)}
	boxes := []Box{
		{ID: "running", Room: "r", State: contract.BoxReady, Size: contract.BoxSize{CPUs: 4, MemBytes: 2 << 30}},
		{ID: "stopped", Room: "r", State: contract.BoxStopped, Size: contract.BoxSize{CPUs: 100, MemBytes: 100 << 30}},
		{ID: "other-room", Room: "other", State: contract.BoxReady, Size: contract.BoxSize{CPUs: 100, MemBytes: 100 << 30}},
	}
	holds := []ServiceHold{
		{ID: "svc_self", Room: "r", CPUMilli: 8000, MemBytes: 16 << 30}, // replaced by the new request
		{ID: "svc_other", Room: "r", CPUMilli: 1000, MemBytes: 512 << 20},
		{ID: "svc_other_room", Room: "other", CPUMilli: 8000, MemBytes: 16 << 30},
	}
	if !ServiceFitsRoom(request, contract.RoomLimits{CPUs: 3, MemBytes: 1536 << 20}, boxes, holds) {
		t.Fatal("one box-quarter plus two whole services fits exactly")
	}
	request.Parts.CPUMilli = 2000
	if ServiceFitsRoom(request, contract.RoomLimits{CPUs: 3}, boxes, holds) {
		t.Fatal("room CPU limit omitted its existing box or service")
	}
	if ServiceFitsRoom(request, contract.RoomLimits{MemBytes: 1280 << 20}, boxes, holds) {
		t.Fatal("room memory limit omitted its existing box or service")
	}
}
