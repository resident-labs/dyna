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
	if admitted["build"] || !admitted["quick"] || !strings.Contains(reasons["build"], "7 GiB free") {
		t.Fatalf("admitted %v, reasons %v", admitted, reasons)
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
