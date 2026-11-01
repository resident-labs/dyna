package jobs

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/machines"
)

func TestQuickLaneUnitLimits(t *testing.T) {
	svc, fake, reporter, _ := newService(t, func(context.Context, string, io.Reader, io.Writer, io.Writer, string, ...string) error { return nil })
	defer svc.Stop()
	job, err := svc.Submit(t.Context(), Input{Command: []string{"true"}, Lane: LaneQuick})
	if err != nil {
		t.Fatal(err)
	}
	if job.Timeout != 10*time.Minute {
		t.Fatalf("timeout = %s", job.Timeout)
	}
	<-reporter.finished
	starts := fake.StartCalls()
	if len(starts) != 1 || starts[0].CPU != "100%" || starts[0].Memory != "1G" || starts[0].IO != "10" {
		t.Fatalf("start limits = %+v", starts)
	}
}

func TestAdmissionUsesAvailableMemoryAndDefaultReservation(t *testing.T) {
	probe := time.Unix(100, 0)
	machine := machines.Machine{Name: "node", LastProbe: probe, Capabilities: machines.Capabilities{CPUs: 32, MemTotal: 30 << 30}, Load: machines.Load{MemTotal: 30 << 30, MemAvailable: 4 << 30}}
	q := nodeQueue{running: []Job{{ID: "running", Host: true, Started: probe.Add(-time.Second)}}, queued: []Job{
		{ID: "one", Host: true, Created: probe},
		{ID: "two", Host: true, Created: probe.Add(time.Second)},
		{ID: "three", Host: true, Created: probe.Add(2 * time.Second)},
	}}
	admitted, reasons := map[string]bool{}, map[string]string{}
	needsWrite := false
	if err := passNode(context.Background(), nil, probe, "node", machine, nil, q, nil, admitted, reasons, &needsWrite); err != nil {
		t.Fatal(err)
	}
	if !admitted["one"] || !admitted["two"] || admitted["three"] || !strings.Contains(reasons["three"], "available") {
		t.Fatalf("admitted %v, reasons %v", admitted, reasons)
	}
	if got := jobDemand(q.running[0], machine).MemoryMiB; got != 1024 {
		t.Fatalf("reservation = %d MiB, want 1024", got)
	}
	if got := ClampResources(true, 0, 0, MachineFacts(machine)).Scope(true).MemoryMiB; got != 5120 {
		t.Fatalf("hard limit = %d MiB, want 5120", got)
	}
	q.running = append(q.running, Job{ID: "one", Host: true, Started: probe.Add(time.Second)}, Job{ID: "two", Host: true, Started: probe.Add(time.Second)})
	q.queued = q.queued[2:]
	admitted, reasons = map[string]bool{}, map[string]string{}
	if err := passNode(context.Background(), nil, probe, "node", machine, nil, q, nil, admitted, reasons, &needsWrite); err != nil {
		t.Fatal(err)
	}
	if admitted["three"] || !strings.Contains(reasons["three"], "available") {
		t.Fatalf("second pass admitted %v, reasons %v", admitted, reasons)
	}
}

func TestAdmissionDoesNotDoubleCountSampledChecks(t *testing.T) {
	probe := time.Unix(100, 0)
	machine := machines.Machine{LastProbe: probe, Capabilities: machines.Capabilities{CPUs: 32, MemTotal: 32 << 30}, Load: machines.Load{MemTotal: 32 << 30, MemAvailable: 24 << 30}}
	q := nodeQueue{heldRows: []admissionRow{{Demand: Demand{CPUs: 2, MemoryMiB: 20 * 1024}, Created: probe.Add(-time.Second)}}, queued: []Job{{ID: "deploy", Host: true, MemoryMiB: 3 * 1024}}}
	admitted, reasons := map[string]bool{}, map[string]string{}
	needsWrite := false
	if err := passNode(context.Background(), nil, probe, "node", machine, nil, q, nil, admitted, reasons, &needsWrite); err != nil {
		t.Fatal(err)
	}
	if !admitted["deploy"] {
		t.Fatalf("sampled check blocked deploy: %v", reasons)
	}
	q.heldRows[0].Created = probe
	admitted, reasons = map[string]bool{}, map[string]string{}
	if err := passNode(context.Background(), nil, probe, "node", machine, nil, q, nil, admitted, reasons, &needsWrite); err != nil {
		t.Fatal(err)
	}
	if admitted["deploy"] {
		t.Fatal("check admitted after the sample must still reserve memory")
	}
}

func TestAdmissionAllowsSmallNodesAndLargeRequests(t *testing.T) {
	for _, tc := range []struct {
		name             string
		total, available uint64
		request          int64
	}{
		{"small node", 2 << 30, 1536 << 20, 0},
		{"large request", 8 << 30, 6 << 30, 7 * 1024},
	} {
		t.Run(tc.name, func(t *testing.T) {
			machine := machines.Machine{Capabilities: machines.Capabilities{CPUs: 8, MemTotal: tc.total}, Load: machines.Load{MemTotal: tc.total, MemAvailable: tc.available}}
			q := nodeQueue{queued: []Job{{ID: "job", Host: true, MemoryMiB: tc.request}}}
			admitted, reasons := map[string]bool{}, map[string]string{}
			needsWrite := false
			if err := passNode(context.Background(), nil, time.Time{}, "node", machine, nil, q, nil, admitted, reasons, &needsWrite); err != nil {
				t.Fatal(err)
			}
			if !admitted["job"] {
				t.Fatalf("job did not start: %v", reasons)
			}
		})
	}
}

func TestQuickLaneReservesMemory(t *testing.T) {
	machine := machines.Machine{Name: "node", Capabilities: machines.Capabilities{CPUs: 8, MemTotal: 8 << 30}}
	q := nodeQueue{queued: []Job{
		{ID: "build", Lane: LaneDeclared, CPUs: 4, MemoryMiB: 8192, Created: time.Unix(1, 0)},
		{ID: "quick", Lane: LaneQuick, Created: time.Unix(2, 0)},
	}}
	admitted, reasons := map[string]bool{}, map[string]string{}
	needsWrite := false
	if err := passNode(context.Background(), nil, time.Time{}, "node", machine, nil, q, nil, admitted, reasons, &needsWrite); err != nil {
		t.Fatal(err)
	}
	resources := ClampResources(false, 4, 8192, MachineFacts(machine))
	if !admitted["build"] || !admitted["quick"] || resources.MemoryMiB != 7168 || !strings.Contains(resources.Note, "memory from 8 GiB to 7 GiB (quick lane reserve)") {
		t.Fatalf("admitted %v, reasons %v, resources %+v", admitted, reasons, resources)
	}
}

func TestQuickLaneAdmission(t *testing.T) {
	machine := machines.Machine{Name: "node", QuickSlots: 1, Capabilities: machines.Capabilities{CPUs: 2, MemTotal: 8 << 30}}
	q := nodeQueue{
		running: []Job{{ID: "build", CPUs: 2, MemoryMiB: 8192}},
		queued: []Job{
			{ID: "quick1", Lane: LaneQuick, Created: time.Unix(1, 0)},
			{ID: "quick2", Lane: LaneQuick, Created: time.Unix(2, 0)},
			{ID: "declared", Lane: LaneDeclared, Created: time.Unix(3, 0)},
		},
	}
	admitted, reasons := map[string]bool{}, map[string]string{}
	needsWrite := false
	if err := passNode(context.Background(), nil, time.Time{}, "node", machine, nil, q, nil, admitted, reasons, &needsWrite); err != nil {
		t.Fatal(err)
	}
	if !admitted["quick1"] || admitted["quick2"] || admitted["declared"] {
		t.Fatalf("admitted: %v", admitted)
	}
	if !strings.Contains(reasons["quick2"], "1 of 1 quick slots in use") {
		t.Fatalf("quick reason: %q", reasons["quick2"])
	}
	if !strings.Contains(reasons["declared"], "CPUs") {
		t.Fatalf("declared reason: %q", reasons["declared"])
	}
}
