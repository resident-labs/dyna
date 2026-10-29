package jobs

import (
	"context"
	"testing"
	"time"

	"dyna-agent/internal/machines"
)

// One read gives a node's capacity, its running jobs and checks, and its
// waiting queue in admission order, with the wait reasons the queue wrote.
func TestWorkReadsCapacityRunningAndWaiting(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	base := time.Now().Add(-time.Hour).Truncate(time.Millisecond)
	node := machines.Machine{Name: "node", Enabled: true, MaxIOHeavy: 1, Resources: map[machines.ResourceName]int{"display": 1}}
	node.Capabilities.CPUs = 8
	node.Capabilities.MemTotal = 16 << 30
	node.Capabilities.Disk = machines.Disk{Total: 100 << 30, Free: 60 << 30}
	queue := Admission{DataDir: dir, Engine: true, clock: func() time.Time { return base.Add(10 * time.Minute) }}
	store := Store{DataDir: dir}
	jobs := []Job{
		{ID: "running", Node: "node", RoomID: "r1", Session: "s1", Command: []string{"make", "test"}, State: StateRunning, Started: base, Created: base, CPUs: 2, MemoryMiB: 2048, Heavy: true, Resources: []machines.ResourceName{"display"}},
		{ID: "late", Node: "node", RoomID: "r1", Command: []string{"late"}, State: StateQueued, Created: base.Add(3 * time.Minute), CPUs: 1, MemoryMiB: 512, WaitReason: "waiting: late"},
		{ID: "other", Node: "elsewhere", State: StateRunning, Command: []string{"x"}, Created: base},
	}
	for _, job := range jobs {
		if err := store.Save(ctx, job); err != nil {
			t.Fatal(err)
		}
	}
	if err := queue.upsert(ctx, Demand{ID: "run-1-lint", Node: "node", Room: "r2", CPUs: 1, MemoryMiB: 1024}, true); err != nil {
		t.Fatal(err)
	}
	if err := queue.upsert(ctx, Demand{ID: "run-2-test", Node: "node", Room: "r2", CPUs: 4, MemoryMiB: 4096}, false); err != nil {
		t.Fatal(err)
	}
	if err := queue.write(ctx, "UPDATE admissions SET reason='waiting: asks 4 CPUs, 3 free' WHERE id='run-2-test'"); err != nil {
		t.Fatal(err)
	}

	work, err := queue.Work(ctx, node)
	if err != nil {
		t.Fatal(err)
	}
	capacity := work.Capacity
	if capacity.CPUs != 8 || capacity.CPUsUsed != 3 || capacity.MemoryMiB != 16384 || capacity.MemoryMiBUsed != 3072 {
		t.Fatalf("capacity = %+v, want 3 of 8 CPUs and 3072 of 16384 MiB held", capacity)
	}
	if capacity.HeavyRunning != 1 || capacity.HeavyLimit != 1 {
		t.Fatalf("IO-heavy = %d of %d, want 1 of 1", capacity.HeavyRunning, capacity.HeavyLimit)
	}
	if len(capacity.Resources) != 1 || capacity.Resources[0] != (ResourceUse{Name: "display", Used: 1, Limit: 1}) {
		t.Fatalf("resources = %+v, want display 1 of 1", capacity.Resources)
	}
	if capacity.DiskFree != 60<<30 || capacity.DiskFloor != 10<<30 || capacity.DiskBlock != "" {
		t.Fatalf("disk = %+v, want 60 GiB free over a 10 GiB floor and no block", capacity)
	}
	if len(work.Running) != 2 || work.Running[0].ID != "running" || work.Running[0].Kind != WorkJob || work.Running[0].Label != "make test" || work.Running[1].Kind != WorkCheck {
		t.Fatalf("running = %+v, want the job then the check", work.Running)
	}
	if len(work.Waiting) != 2 {
		t.Fatalf("waiting = %+v, want the late job and the check", work.Waiting)
	}
	// The late job is older than the check's row, and the two rooms take turns.
	first, second := work.Waiting[0], work.Waiting[1]
	if first.ID != "late" || first.Position != 1 || first.Reason != "waiting: late" {
		t.Fatalf("first waiter = %+v", first)
	}
	if second.ID != "run-2-test" || second.Position != 2 || second.Reason != "waiting: asks 4 CPUs, 3 free" {
		t.Fatalf("second waiter = %+v", second)
	}
}
