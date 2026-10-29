package jobs

import (
	"context"
	"testing"
	"time"

	"dyna-agent/internal/hub"
	"dyna-agent/internal/machines"
	"dyna-agent/internal/state"
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
	if capacity.DiskFree != 60<<30 || capacity.DiskKeeps != "a tenth of the disk" || capacity.DiskBlock != "" {
		t.Fatalf("disk = %+v, want 60 GiB free, a tenth of the disk kept and no block", capacity)
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

// A read must not publish. An open machine page rereads on every admission
// event, so a read that wrote one would keep the page and the database busy.
func TestWorkReadPublishesOnlyWhenItSweepsARow(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	queue := Admission{DataDir: t.TempDir(), Engine: true, clock: func() time.Time { return now }}
	node := machines.Machine{Name: "node", Enabled: true}
	if err := queue.upsert(ctx, Demand{ID: "held", Node: "node", CPUs: 1, Timeout: time.Minute}, true); err != nil {
		t.Fatal(err)
	}
	db, err := queue.db()
	if err != nil {
		t.Fatal(err)
	}
	writer := state.For(db)
	writer.WaitPublished()
	var events []hub.Event
	writer.SetPublish(func(ev hub.Event) { events = append(events, ev) })
	if _, err := queue.Work(ctx, node); err != nil {
		t.Fatal(err)
	}
	writer.WaitPublished()
	if len(events) != 0 {
		t.Fatalf("an idle read published %v", events)
	}
	queue.clock = func() time.Time { return now.Add(time.Hour) }
	if _, err := queue.Work(ctx, node); err != nil {
		t.Fatal(err)
	}
	writer.WaitPublished()
	if len(events) != 1 || events[0].Topic != AdmissionTopic {
		t.Fatalf("sweeping an expired row published %v, want one admission event", events)
	}
}

// A holder that admits its row between the sweep's read and its delete keeps
// the reservation: the sweep deletes only a row still waiting for the dead owner.
func TestSweepKeepsRowAdmittedWhileItRuns(t *testing.T) {
	ctx := context.Background()
	queue := Admission{DataDir: t.TempDir()}
	demand := Demand{ID: "check", Node: "node", CPUs: 1}
	if err := queue.upsert(ctx, demand, false); err != nil {
		t.Fatal(err)
	}
	oldAlive := ownerAlive
	t.Cleanup(func() { ownerAlive = oldAlive })
	ownerAlive = func(int, uint64) bool {
		if err := queue.upsert(ctx, demand, true); err != nil {
			t.Error(err)
		}
		return false
	}
	if err := queue.sweep(ctx); err != nil {
		t.Fatal(err)
	}
	row, err := queue.get(ctx, "check")
	if err != nil || row.State != "admitted" {
		t.Fatalf("row = %+v, err = %v, want the admitted reservation kept", row, err)
	}
}
