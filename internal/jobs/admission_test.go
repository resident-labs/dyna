package jobs

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"dyna-agent/internal/machines"
)

type gatedMachines struct {
	machine machines.Machine
	calls   atomic.Int32
	entered chan struct{}
	resume  chan struct{}
}

func (g *gatedMachines) Get(_ context.Context, _ string) (machines.Machine, error) {
	if g.calls.Add(1) == 1 {
		close(g.entered)
		<-g.resume
	}
	return g.machine, nil
}
func (g *gatedMachines) GetLocal(context.Context) (machines.Machine, error) {
	return g.machine, nil
}

// A concurrent check that reserves the last CPUs before the job's decision
// must be counted by the job's pass, even if both started together.
func TestAdmissionSerializesConcurrentPasses(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	node := machines.Machine{Name: "node", Enabled: true}
	node.Capabilities.CPUs = 2
	source := &gatedMachines{machine: node, entered: make(chan struct{}), resume: make(chan struct{})}
	queue := Admission{DataDir: dir, Machines: source, Engine: true}
	store := Store{DataDir: dir}
	job := Job{ID: "job", Node: "node", State: StateQueued, CPUs: 2, Host: true, Created: time.Now()}
	if err := store.Save(ctx, job); err != nil {
		t.Fatal(err)
	}
	type result struct {
		admitted map[string]bool
		err      error
	}
	done := make(chan result, 1)
	go func() {
		admitted, _, err := queue.Pass(ctx, []Job{job})
		done <- result{admitted, err}
	}()
	<-source.entered
	if err := queue.upsert(ctx, Demand{ID: "check", Node: "node", CPUs: 2}, false); err != nil {
		t.Fatal(err)
	}
	_, _, err := queue.Pass(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	close(source.resume)
	got := <-done
	if got.err != nil {
		t.Fatal(got.err)
	}
	if got.admitted[job.ID] {
		t.Fatal("job admitted after check reserved the last CPUs")
	}
}

// A job reserves capacity before its goroutine starts. A concurrent check must
// not also take that capacity in the gap between admission and launch.
func TestAdmissionReservesJobBeforeLaunch(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	node := machines.Machine{Name: "node", Enabled: true}
	node.Capabilities.CPUs = 2
	source := fakeMachines{byName: map[string]machines.Machine{"node": node}}
	queue := Admission{DataDir: dir, Machines: source, Engine: true}
	store := Store{DataDir: dir}
	job := Job{ID: "job", Node: "node", State: StateQueued, CPUs: 2, Host: true, Created: time.Now()}
	if err := store.Save(ctx, job); err != nil {
		t.Fatal(err)
	}
	admitted, _, err := queue.Pass(ctx, []Job{job})
	if err != nil || !admitted[job.ID] {
		t.Fatalf("job admission: %v, %v", admitted, err)
	}
	if err := queue.upsert(ctx, Demand{ID: "check", Node: "node", CPUs: 2}, false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := queue.Pass(ctx, nil); err != nil {
		t.Fatal(err)
	}
	check, err := queue.get(ctx, "check")
	if err != nil {
		t.Fatal(err)
	}
	if check.State != "waiting" {
		t.Fatalf("check state = %s, want waiting", check.State)
	}
}

// An abandoned check stops holding capacity and its port after its deadline.
func TestAdmissionExpiresAbandonedCheck(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1000, 0)
	node := machines.Machine{Name: "node", Enabled: true}
	node.Capabilities.CPUs = 2
	queue := Admission{DataDir: t.TempDir(), Engine: true,
		Machines: fakeMachines{byName: map[string]machines.Machine{"node": node}},
		clock:    func() time.Time { return now }}
	old := Demand{ID: "old", Node: "node", CPUs: 2, Timeout: time.Minute}
	if _, err := queue.Hold(ctx, old); err != nil {
		t.Fatal(err)
	}
	next := Demand{ID: "next", Node: "node", CPUs: 2, Timeout: time.Minute}
	if err := queue.upsert(ctx, next, false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := queue.Pass(ctx, nil); err != nil {
		t.Fatal(err)
	}
	row, err := queue.get(ctx, next.ID)
	if err != nil || row.State != "waiting" {
		t.Fatalf("before expiry: %+v, %v", row, err)
	}
	// A healthy check renews its place before the original expiry.
	now = now.Add(5 * time.Minute)
	if err := queue.Renew(ctx, old.Node, old.ID); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	if _, _, err := queue.Pass(ctx, nil); err != nil {
		t.Fatal(err)
	}
	row, err = queue.get(ctx, next.ID)
	if err != nil || row.State != "waiting" {
		t.Fatalf("after renewal: %+v, %v", row, err)
	}
	// Once polling stops, the renewed reservation eventually expires.
	now = now.Add(10 * time.Minute)
	if _, _, err := queue.Pass(ctx, nil); err != nil {
		t.Fatal(err)
	}
	row, err = queue.get(ctx, next.ID)
	if err != nil || row.State != "admitted" || row.Port != 0 {
		t.Fatalf("after expiry: %+v, %v", row, err)
	}
	if _, err := queue.Reattach(ctx, old); err == nil {
		t.Fatal("reattached an expired check after its port was reassigned")
	}
}

// A restart must retain detached checks' existing port reservations, even if
// another check starts before an old check reattaches.
func TestAdmissionRestartPreservesCheckPorts(t *testing.T) {
	ctx := context.Background()
	queue := Admission{DataDir: t.TempDir(), Engine: true}
	a := Demand{ID: "a", Node: "node"}
	b := Demand{ID: "b", Node: "node"}
	portA, err := queue.Hold(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	portB, err := queue.Hold(ctx, b)
	if err != nil {
		t.Fatal(err)
	}
	if err := queue.ResetEngine(ctx); err != nil {
		t.Fatal(err)
	}
	c := Demand{ID: "c", Node: "node"}
	portC, err := queue.Hold(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	again, err := queue.Hold(ctx, b)
	if err != nil {
		t.Fatal(err)
	}
	if portA != 0 || portB != 1 || portC != 2 || again != portB {
		t.Fatalf("ports = %d, %d, %d, %d; want 0, 1, 2, 1", portA, portB, portC, again)
	}
}
