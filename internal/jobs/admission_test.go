package jobs

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/machines"
)

// A database written before check names still opens, from either earlier
// schema: the queue migrates its table, and a row it holds reads with its ID
// and no source until it ages out.
func TestAdmissionMigratesRowsWrittenBeforeCheckNames(t *testing.T) {
	ctx := context.Background()
	const v1 = `CREATE TABLE admissions (
	id TEXT PRIMARY KEY, node TEXT NOT NULL, room TEXT NOT NULL DEFAULT '',
	cpus INTEGER NOT NULL DEFAULT 0, memory_mib INTEGER NOT NULL DEFAULT 0,
	heavy INTEGER NOT NULL DEFAULT 0, port INTEGER NOT NULL DEFAULT -1,
	state TEXT NOT NULL, reason TEXT NOT NULL DEFAULT '', engine INTEGER NOT NULL DEFAULT 0,
	owner_pid INTEGER NOT NULL DEFAULT 0, owner_start INTEGER NOT NULL DEFAULT 0,
	timeout_ms INTEGER NOT NULL DEFAULT 1800000, expires_ms INTEGER NOT NULL DEFAULT 0,
	created_ms INTEGER NOT NULL);
INSERT INTO meta(key,value) VALUES ('admissions.version','1');`
	const v2 = `CREATE TABLE admissions (
	id TEXT PRIMARY KEY, node TEXT NOT NULL, room TEXT NOT NULL DEFAULT '',
	cpus INTEGER NOT NULL DEFAULT 0, memory_mib INTEGER NOT NULL DEFAULT 0,
	heavy INTEGER NOT NULL DEFAULT 0, ahead INTEGER NOT NULL DEFAULT 0,
	port INTEGER NOT NULL DEFAULT -1, state TEXT NOT NULL, reason TEXT NOT NULL DEFAULT '',
	engine INTEGER NOT NULL DEFAULT 0, owner_pid INTEGER NOT NULL DEFAULT 0,
	owner_start INTEGER NOT NULL DEFAULT 0, timeout_ms INTEGER NOT NULL DEFAULT 1800000,
	expires_ms INTEGER NOT NULL DEFAULT 0, created_ms INTEGER NOT NULL);
INSERT INTO meta(key,value) VALUES ('admissions.version','2');`
	for name, schema := range map[string]string{"from v1": v1, "from v2": v2} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			db, err := database.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.ExecContext(ctx, schema+`
INSERT INTO admissions(id,node,room,cpus,memory_mib,heavy,port,state,created_ms,expires_ms) VALUES ('old-check','node','r1',1,512,0,-1,'admitted',1,2000000000000)`); err != nil {
				t.Fatal(err)
			}
			queue := Admission{DataDir: dir}
			work, err := queue.Work(ctx, machines.Machine{Name: "node", Enabled: true})
			if err != nil {
				t.Fatal(err)
			}
			if len(work.Running) != 1 || work.Running[0].ID != "old-check" || work.Running[0].Label != "old-check" || work.Running[0].Source != nil {
				t.Fatalf("running = %+v, want the old row named by its ID with no source", work.Running)
			}
			if err := queue.upsert(ctx, Demand{ID: "new-check", Node: "node", Check: "lint", Source: CheckSource{Kind: CheckKindPullRequest, Number: 7}}, false); err != nil {
				t.Fatal(err)
			}
			work, err = queue.Work(ctx, machines.Machine{Name: "node", Enabled: true})
			if err != nil {
				t.Fatal(err)
			}
			if len(work.Waiting) != 1 || work.Waiting[0].Label != "lint" || work.Waiting[0].Source == nil || work.Waiting[0].Source.Number != 7 {
				t.Fatalf("waiting = %+v, want the new row named lint with its source", work.Waiting)
			}
		})
	}
}

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
func (g *gatedMachines) List(context.Context) ([]machines.Machine, error) {
	return []machines.Machine{g.machine}, nil
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

// A manual runner can die while its detached check continues on the node.
// Its admitted reservation must survive, but its abandoned waiter must not.
func TestAdmissionRetainsDetachedManualCheck(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1000, 0)
	node := machines.Machine{Name: "node", Enabled: true}
	node.Capabilities.CPUs = 2
	queue := Admission{DataDir: t.TempDir(), Machines: fakeMachines{byName: map[string]machines.Machine{"node": node}}, clock: func() time.Time { return now }}
	if _, err := queue.Hold(ctx, Demand{ID: "running", Node: "node", CPUs: 2, Timeout: time.Minute}); err != nil {
		t.Fatal(err)
	}
	if err := queue.upsert(ctx, Demand{ID: "abandoned", Node: "node", CPUs: 2}, false); err != nil {
		t.Fatal(err)
	}
	if err := queue.write(ctx, "UPDATE admissions SET owner_pid=0"); err != nil {
		t.Fatal(err)
	}
	next := Demand{ID: "next", Node: "node", CPUs: 2}
	if err := queue.upsert(ctx, next, false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := queue.Pass(ctx, nil); err != nil {
		t.Fatal(err)
	}
	row, err := queue.get(ctx, next.ID)
	if err != nil || row.State != "waiting" {
		t.Fatalf("check after runner death = %+v, %v; want waiting", row, err)
	}
	if _, err := queue.get(ctx, "abandoned"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("abandoned waiter error = %v, want not found", err)
	}
	now = now.Add(7 * time.Minute)
	if _, _, err := queue.Pass(ctx, nil); err != nil {
		t.Fatal(err)
	}
	row, err = queue.get(ctx, next.ID)
	if err != nil || row.State != "admitted" || row.Port != 0 {
		t.Fatalf("check after expiry = %+v, %v; want admitted on port 0", row, err)
	}
}

// A new manual runner must be able to release the reservation it reattaches.
func TestAdmissionReattachedManualRunnerCanRelease(t *testing.T) {
	ctx := context.Background()
	queue := Admission{DataDir: t.TempDir()}
	demand := Demand{ID: "running", Node: "node"}
	if _, err := queue.Hold(ctx, demand); err != nil {
		t.Fatal(err)
	}
	if err := queue.write(ctx, "UPDATE admissions SET owner_pid=0 WHERE id=?", demand.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := queue.Reattach(ctx, demand); err != nil {
		t.Fatal(err)
	}
	if err := queue.Release(ctx, demand.Node, demand.ID); err != nil {
		t.Fatal(err)
	}
	row, err := queue.get(ctx, demand.ID)
	if err != nil || row.State != "released" {
		t.Fatalf("reservation after release = %+v, %v", row, err)
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

// A running job holds its requested resource after a restart. The waiter
// names that holder and starts when the holder's stored run ends.
func TestAdmissionResourceWaitNamesHolderAndReleases(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	node := machines.Machine{Name: "node", Enabled: true, Resources: map[machines.ResourceName]int{"ghidra": 1}}
	queue := Admission{
		DataDir: dir, Machines: fakeMachines{byName: map[string]machines.Machine{"node": node}}, Engine: true,
		RoomName: func(string) string { return "cozycraft" },
	}
	store := Store{DataDir: dir}
	first := Job{ID: "job_ab12", RoomID: "room_cozycraft", Node: "node", State: StateRunning, Host: true, Created: time.Now(), Resources: []machines.ResourceName{"ghidra"}}
	second := Job{ID: "job_cd34", RoomID: "room_cozycraft", Node: "node", State: StateQueued, Host: true, Created: first.Created.Add(time.Second), Resources: []machines.ResourceName{"ghidra"}}
	for _, job := range []Job{first, second} {
		if err := store.Save(ctx, job); err != nil {
			t.Fatal(err)
		}
	}
	admitted, reasons, err := queue.Pass(ctx, []Job{second})
	if err != nil {
		t.Fatal(err)
	}
	if admitted[second.ID] {
		t.Fatal("waiter admitted while the resource was held")
	}
	want := "waiting: resource ghidra held by job_ab12 (room cozycraft), position 1 of 1"
	if reasons[second.ID] != want {
		t.Fatalf("wait reason = %q, want %q", reasons[second.ID], want)
	}
	first.State = StatePassed
	if err := store.Save(ctx, first); err != nil {
		t.Fatal(err)
	}
	admitted, _, err = queue.Pass(ctx, []Job{second})
	if err != nil || !admitted[second.ID] {
		t.Fatalf("admission after release = %v, %v", admitted, err)
	}
}

// A resource name with no node declaration is a one-unit lock.
func TestAdmissionUndeclaredResourceIsOneUnitLock(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	node := machines.Machine{Name: "node", Enabled: true}
	queue := Admission{DataDir: dir, Machines: fakeMachines{byName: map[string]machines.Machine{"node": node}}, Engine: true}
	store := Store{DataDir: dir}
	now := time.Now()
	first := Job{ID: "job_a", Node: "node", State: StateQueued, Host: true, Created: now, Resources: []machines.ResourceName{"cozybodies-test"}}
	second := Job{ID: "job_b", Node: "node", State: StateQueued, Host: true, Created: now.Add(time.Second), Resources: []machines.ResourceName{"cozybodies-test"}}
	for _, job := range []Job{first, second} {
		if err := store.Save(ctx, job); err != nil {
			t.Fatal(err)
		}
	}
	admitted, reasons, err := queue.Pass(ctx, []Job{first, second})
	if err != nil {
		t.Fatal(err)
	}
	if !admitted[first.ID] || admitted[second.ID] {
		t.Fatalf("default lock admissions = %v, want only the first job", admitted)
	}
	if !strings.Contains(reasons[second.ID], "resource cozybodies-test held by job_a") {
		t.Fatalf("default lock wait reason = %q", reasons[second.ID])
	}
}

// A declared count admits that many holders of the same resource.
func TestAdmissionDeclaredResourceCountAdmitsTwo(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	node := machines.Machine{Name: "node", Enabled: true, Resources: map[machines.ResourceName]int{"gpu-slot": 2}}
	queue := Admission{DataDir: dir, Machines: fakeMachines{byName: map[string]machines.Machine{"node": node}}, Engine: true}
	store := Store{DataDir: dir}
	now := time.Now()
	jobs := []Job{
		{ID: "job_a", Node: "node", State: StateQueued, Host: true, Created: now, Resources: []machines.ResourceName{"gpu-slot"}},
		{ID: "job_b", Node: "node", State: StateQueued, Host: true, Created: now.Add(time.Second), Resources: []machines.ResourceName{"gpu-slot"}},
		{ID: "job_c", Node: "node", State: StateQueued, Host: true, Created: now.Add(2 * time.Second), Resources: []machines.ResourceName{"gpu-slot"}},
	}
	for _, job := range jobs {
		if err := store.Save(ctx, job); err != nil {
			t.Fatal(err)
		}
	}
	admitted, _, err := queue.Pass(ctx, jobs)
	if err != nil {
		t.Fatal(err)
	}
	if !admitted["job_a"] || !admitted["job_b"] || admitted["job_c"] {
		t.Fatalf("two-unit resource admissions = %v, want job_a and job_b only", admitted)
	}
}

// A room's ahead check, a merge trial's, is admitted before that room's older
// waiting checks when the node frees: a merge queue must never wait behind its
// own room's pull-request CI. Other rooms still take their turns.
func TestAdmissionAdmitsARoomsAheadCheckBeforeItsOlderWaitingChecks(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	node := machines.Machine{Name: "node", Enabled: true}
	node.Capabilities.CPUs = 2
	queue := Admission{DataDir: dir, Machines: fakeMachines{byName: map[string]machines.Machine{"node": node}}, Engine: true}
	holder := Demand{ID: "holder", Node: "node", Room: "room", CPUs: 2, Timeout: time.Minute}
	if _, err := queue.Hold(ctx, holder); err != nil {
		t.Fatal(err)
	}
	prHead := Demand{ID: "pr-head", Node: "node", Room: "room", CPUs: 2, Timeout: time.Minute}
	if err := queue.upsert(ctx, prHead, false); err != nil {
		t.Fatal(err)
	}
	trial := Demand{ID: "trial", Node: "node", Room: "room", CPUs: 2, Timeout: time.Minute, Ahead: true}
	if err := queue.upsert(ctx, trial, false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := queue.Pass(ctx, nil); err != nil {
		t.Fatal(err)
	}
	// Capacity frees, and only one of the two waiting checks fits.
	if err := queue.Release(ctx, holder.Node, holder.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := queue.Pass(ctx, nil); err != nil {
		t.Fatal(err)
	}
	row, err := queue.get(ctx, trial.ID)
	if err != nil || row.State != "admitted" {
		t.Fatalf("trial check = %+v, %v; want admitted ahead of the room's waiting checks", row, err)
	}
	row, err = queue.get(ctx, prHead.ID)
	if err != nil || row.State != "waiting" {
		t.Fatalf("pull-request check = %+v, %v; want still waiting", row, err)
	}
}
