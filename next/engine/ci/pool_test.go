package ci

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

const repo contract.RepoName = "owner/repo"

type fakeStore struct {
	settings []contract.RepoCIHost
	hosts    []box.Host
	boxes    []box.Box
}

func (s *fakeStore) CIHosts(context.Context) ([]contract.RepoCIHost, error) { return s.settings, nil }
func (s *fakeStore) Hosts(context.Context) ([]box.Host, error)              { return s.hosts, nil }
func (s *fakeStore) Boxes(context.Context) ([]box.Box, error)               { return s.boxes, nil }

type fakeDriver struct {
	runners  []box.Runner
	egress   bool
	builds   int
	specs    []box.RunnerSpec
	removed  []string
	startErr error
	listErr  error
	listCtx  context.Context
}

func (d *fakeDriver) BuildRunnerImage(context.Context) (string, error) {
	d.builds++
	return "image", nil
}
func (d *fakeDriver) EnsureEgress(context.Context, string) error { d.egress = true; return nil }
func (d *fakeDriver) RemoveEgress(context.Context) error         { d.egress = false; return nil }
func (d *fakeDriver) StartRunner(_ context.Context, s box.RunnerSpec) error {
	d.specs = append(d.specs, s)
	if d.startErr != nil {
		return d.startErr
	}
	d.runners = append(d.runners, box.Runner{Name: s.Name, Repo: s.Repo, ID: s.ID, Running: true})
	return nil
}
func (d *fakeDriver) ListRunners(ctx context.Context) ([]box.Runner, bool, error) {
	d.listCtx = ctx
	return slices.Clone(d.runners), d.egress, d.listErr
}
func (d *fakeDriver) RemoveRunner(_ context.Context, name string) error {
	d.removed = append(d.removed, name)
	d.runners = slices.DeleteFunc(d.runners, func(r box.Runner) bool { return r.Name == name })
	return nil
}

type fakeAPI struct {
	deleted []int64
	listed  int
	leases  []time.Time
	list    []Registration
}

func (a *fakeAPI) DeleteRunner(_ context.Context, _ contract.RepoName, id int64) error {
	a.deleted = append(a.deleted, id)
	return nil
}
func (a *fakeAPI) ListRunners(context.Context, contract.RepoName) ([]Registration, error) {
	a.listed++
	return a.list, nil
}
func (a *fakeAPI) RenewLease(_ context.Context, _ contract.RepoName, until time.Time) error {
	a.leases = append(a.leases, until)
	return nil
}

type fakeSets struct {
	mu        sync.Mutex
	next      int64
	names     []string
	jitErr    error
	listening map[contract.RepoName]int
	listenErr error
	deleted   []contract.RepoName
}

func (s *fakeSets) Listen(ctx context.Context, repo contract.RepoName, _ Scale) error {
	s.mu.Lock()
	if s.listening == nil {
		s.listening = make(map[contract.RepoName]int)
	}
	s.listening[repo]++
	err := s.listenErr
	s.mu.Unlock()
	if err != nil {
		return err
	}
	<-ctx.Done()
	return nil
}
func (s *fakeSets) JIT(_ context.Context, _ contract.RepoName, name string) (int64, string, error) {
	if s.jitErr != nil {
		return 0, "", s.jitErr
	}
	s.next++
	s.names = append(s.names, name)
	return s.next, "jit-secret", nil
}
func (s *fakeSets) Delete(_ context.Context, repo contract.RepoName) error {
	s.deleted = append(s.deleted, repo)
	return nil
}

type fixture struct {
	pool    *Pool
	store   *fakeStore
	drivers map[contract.HostName]*fakeDriver
	api     *fakeAPI
	sets    *fakeSets
	now     time.Time
	group   sync.WaitGroup
}

func setting(host contract.HostName, runners int) contract.RepoCIHost {
	return contract.RepoCIHost{Repo: repo, Host: host, Runners: runners, Size: contract.BoxSizeLarge, Room: "room"}
}

func readyHost(name contract.HostName, memory int64) box.Host {
	return box.Host{Name: name, State: box.HostReady, CPUs: 16, MemBytes: memory, DiskBytes: 100 * box.GiB, DiskFloor: 20 * box.GiB, CheckedAt: time.Unix(1, 0)}
}

func newFixture(runners int) *fixture {
	f := &fixture{api: &fakeAPI{}, sets: &fakeSets{}, now: time.Unix(1_000_000, 0), drivers: map[contract.HostName]*fakeDriver{"pve": {}}}
	f.store = &fakeStore{settings: []contract.RepoCIHost{setting("pve", runners)}, hosts: []box.Host{readyHost("pve", 64*box.GiB)}}
	f.pool = &Pool{Store: f.store, GitHub: f.api, Sets: f.sets, Now: func() time.Time { return f.now },
		Hosts: func(_ context.Context, name contract.HostName) (Driver, error) { return f.drivers[name], nil }}
	return f
}

func (f *fixture) scale(assigned int) int {
	return f.pool.scale(context.Background(), repo, assigned)
}

func (f *fixture) tick(t *testing.T) {
	t.Helper()
	if err := f.pool.Tick(context.Background(), &f.group); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) stop() {
	for _, l := range f.pool.listeners {
		l.cancel()
	}
	f.group.Wait()
}

func TestRunnersFollowAssignedJobsUpToHostCapacity(t *testing.T) {
	f := newFixture(2)
	if capacity := f.scale(0); capacity != 2 || len(f.sets.names) != 0 {
		t.Fatalf("idle pool: capacity %d, registered %v", capacity, f.sets.names)
	}
	f.scale(1)
	if len(f.drivers["pve"].runners) != 1 || !f.drivers["pve"].egress || f.drivers["pve"].builds != 1 {
		t.Fatalf("one job: %+v", f.drivers["pve"])
	}
	spec := f.drivers["pve"].specs[0]
	if spec.Config != "jit-secret" || spec.Repo != repo || spec.Size.Name != contract.BoxSizeLarge || !strings.HasPrefix(spec.Name, "dyna-ci-pve-") {
		t.Fatalf("spec %+v", spec)
	}
	f.scale(1)
	if len(f.sets.names) != 1 {
		t.Fatalf("the same job started %d runners", len(f.sets.names))
	}
	f.scale(5)
	if len(f.drivers["pve"].runners) != 2 || f.drivers["pve"].builds != 1 {
		t.Fatalf("host cap: %d runners, %d builds", len(f.drivers["pve"].runners), f.drivers["pve"].builds)
	}
}

func TestFinishedRunnerLeavesAndTheNextJobGetsANewOne(t *testing.T) {
	f := newFixture(1)
	f.scale(1)
	f.drivers["pve"].runners = nil // --rm destroyed it when its job ended
	f.scale(0)
	if len(f.sets.names) != 1 {
		t.Fatalf("started a runner with no job: %v", f.sets.names)
	}
	f.scale(1)
	if len(f.sets.names) != 2 {
		t.Fatalf("registered %d", len(f.sets.names))
	}
}

func TestAdmissionPicksTheHostWithMostMemoryAndSpills(t *testing.T) {
	f := newFixture(1)
	f.drivers["big"] = &fakeDriver{}
	f.store.hosts = append(f.store.hosts, readyHost("big", 128*box.GiB))
	f.store.settings = append(f.store.settings, setting("big", 1))
	if capacity := f.scale(1); capacity != 2 {
		t.Fatalf("capacity %d", capacity)
	}
	if len(f.drivers["big"].runners) != 1 || len(f.drivers["pve"].runners) != 0 {
		t.Fatalf("big %d, pve %d", len(f.drivers["big"].runners), len(f.drivers["pve"].runners))
	}
	f.scale(2)
	if len(f.drivers["pve"].runners) != 1 {
		t.Fatal("the second job did not spill to the other host")
	}
}

func TestAddingAHostIsOnlyASetting(t *testing.T) {
	f := newFixture(1)
	f.drivers["laptop"] = &fakeDriver{}
	f.store.hosts = append(f.store.hosts, readyHost("laptop", 32*box.GiB))
	f.scale(1)
	if len(f.drivers["laptop"].runners) != 0 {
		t.Fatal("a host no repository named got a runner")
	}
	f.store.settings = append(f.store.settings, setting("laptop", 1))
	f.scale(2)
	if len(f.drivers["laptop"].runners) != 1 || f.drivers["laptop"].builds != 1 || !f.drivers["laptop"].egress {
		t.Fatalf("laptop %+v", f.drivers["laptop"])
	}
}

func TestRoomBoundHostRefusesAnotherRoomsRepository(t *testing.T) {
	f := newFixture(1)
	f.store.hosts[0].Rooms = []contract.RoomID{"other"}
	if capacity := f.scale(1); capacity != 0 || len(f.sets.names) != 0 {
		t.Fatalf("capacity %d, registered %v", capacity, f.sets.names)
	}
}

func TestBelowTheDiskFloorCapacityFallsToWhatIsRunning(t *testing.T) {
	f := newFixture(3)
	f.drivers["pve"].runners = []box.Runner{{Name: "dyna-ci-pve-working", Repo: repo, ID: 7, Running: true}}
	f.store.hosts[0].DiskBytes = 20*box.GiB + 40*box.GiB - 1 // the floor plus a large request, less one byte
	if capacity := f.scale(3); capacity != 1 {
		t.Fatalf("capacity %d, want only the working runner", capacity)
	}
	if len(f.sets.names) != 0 || f.drivers["pve"].builds != 0 || len(f.drivers["pve"].removed) != 0 {
		t.Fatalf("registered %v, removed %v", f.sets.names, f.drivers["pve"].removed)
	}
	f.store.hosts[0].DiskBytes = 20*box.GiB + 40*box.GiB
	if capacity := f.scale(3); capacity != 3 || len(f.sets.names) != 2 {
		t.Fatalf("at the floor: capacity %d, registered %v", capacity, f.sets.names)
	}
}

func TestHostMemoryPressureStopsNewRunners(t *testing.T) {
	f := newFixture(2)
	f.pool.Memory = func(contract.HostName) int64 { return 60 * box.GiB }
	if capacity := f.scale(1); capacity != 0 || len(f.sets.names) != 0 {
		t.Fatalf("capacity %d, registered %v", capacity, f.sets.names)
	}
}

func TestSessionBoxesCountAgainstCIPlacement(t *testing.T) {
	f := newFixture(5)
	large, _ := box.Size(contract.BoxSizeLarge)
	f.store.boxes = []box.Box{{ID: "b", Host: "pve", State: contract.BoxReady, Image: "image", Size: contract.BoxSize{Name: large.Name, CPUs: 60, MemBytes: large.MemBytes}}}
	f.scale(5)
	// 4 x 16 CPUs is 64, the box takes 60, and no large runner fits in the rest.
	if len(f.sets.names) != 0 {
		t.Fatalf("registered %v past the host's CPUs", f.sets.names)
	}
}

// A laptop drops off the tailnet mid-job. The pool stops seeing its runners and
// offers no capacity there. GitHub fails the job when the runner stops
// answering. The registration of an idle runner goes at the next reap, and that
// runner exits by itself when the host comes back (proved on a live runner).
func TestHostDroppingOutLosesCapacityAndReapsItsIdleRunners(t *testing.T) {
	f := newFixture(1)
	f.drivers["laptop"] = &fakeDriver{}
	f.store.hosts = append(f.store.hosts, readyHost("laptop", 128*box.GiB))
	f.store.settings = append(f.store.settings, setting("laptop", 2))
	f.scale(2)
	laptop := f.drivers["laptop"]
	if len(laptop.runners) != 2 {
		t.Fatalf("laptop %d", len(laptop.runners))
	}
	idle, busy := laptop.runners[0], laptop.runners[1]
	f.tick(t)

	f.store.hosts[1].State = box.HostUnreachable
	if capacity := f.scale(0); capacity != 1 {
		t.Fatalf("capacity %d after the drop", capacity)
	}
	f.now = f.now.Add(reapEvery)
	f.api.list = []Registration{{ID: idle.ID, Name: idle.Name, Status: "offline"}, {ID: busy.ID, Name: busy.Name, Status: "offline", Busy: true}}
	f.tick(t)
	if !slices.Equal(f.api.deleted, []int64{idle.ID}) {
		t.Fatalf("deleted %v, want only the idle runner", f.api.deleted)
	}
	if len(laptop.removed) != 0 {
		t.Fatal("touched a host it cannot reach")
	}
	// The host is back. Its idle runner has exited; its busy one still holds a slot.
	f.store.hosts[1].State = box.HostReady
	laptop.runners = laptop.runners[1:]
	if capacity := f.scale(2); capacity != 3 {
		t.Fatalf("capacity %d after the return", capacity)
	}
	f.stop()
}

func TestHostThatStopsAnsweringIsSkippedForTheOthers(t *testing.T) {
	f := newFixture(1)
	f.drivers["laptop"] = &fakeDriver{listErr: errors.New("ssh failed")}
	f.store.hosts = append(f.store.hosts, readyHost("laptop", 128*box.GiB))
	f.store.settings = append(f.store.settings, setting("laptop", 4))
	if capacity := f.scale(1); capacity != 1 || len(f.drivers["pve"].runners) != 1 {
		t.Fatalf("capacity %d, pve %d", capacity, len(f.drivers["pve"].runners))
	}
}

func TestFailedStartDeletesRegistrationAndTriesAnotherHost(t *testing.T) {
	f := newFixture(1)
	f.drivers["big"] = &fakeDriver{startErr: errors.New("podman failed")}
	f.store.hosts = append(f.store.hosts, readyHost("big", 128*box.GiB))
	f.store.settings = append(f.store.settings, setting("big", 1))
	f.scale(1)
	if !slices.Equal(f.api.deleted, []int64{1}) || len(f.drivers["pve"].runners) != 1 {
		t.Fatalf("deleted %v, pve %d", f.api.deleted, len(f.drivers["pve"].runners))
	}
	f.drivers["pve"].runners = nil
	f.scale(1)
	if len(f.drivers["big"].specs) != 1 {
		t.Fatalf("retried the failed host inside the backoff: %d", len(f.drivers["big"].specs))
	}
	f.drivers["big"].startErr = nil
	f.drivers["pve"].runners = nil
	f.now = f.now.Add(retryAfter)
	f.scale(1)
	if len(f.drivers["big"].runners) != 1 || f.drivers["big"].builds != 2 {
		t.Fatalf("big %+v", f.drivers["big"])
	}
}

func TestFailedJITStartsNothingAndKeepsTheListener(t *testing.T) {
	f := newFixture(1)
	f.sets.jitErr = errors.New("service unavailable")
	if capacity := f.scale(1); capacity != 1 || len(f.drivers["pve"].specs) != 0 {
		t.Fatalf("capacity %d, specs %d", capacity, len(f.drivers["pve"].specs))
	}
}

func TestOneListenerPerRepositoryRestartsAfterFailure(t *testing.T) {
	f := newFixture(1)
	f.sets.listenErr = errors.New("session conflict")
	f.tick(t)
	f.group.Wait()
	f.sets.mu.Lock()
	f.sets.listenErr = nil
	f.sets.mu.Unlock()
	if err := f.pool.Tick(context.Background(), &f.group); err == nil || !strings.Contains(err.Error(), "session conflict") {
		t.Fatalf("error %v", err)
	}
	f.tick(t)
	f.tick(t)
	f.sets.mu.Lock()
	starts := f.sets.listening[repo]
	f.sets.mu.Unlock()
	if starts > 2 {
		t.Fatalf("started %d listeners", starts)
	}
	f.stop()
}

func TestDisablingRemovesRunnersAndTheScaleSetThenGoesQuiet(t *testing.T) {
	f := newFixture(2)
	f.tick(t)
	f.scale(2)
	f.store.settings = nil
	f.tick(t) // cancels the listener and removes the runners
	if len(f.drivers["pve"].runners) != 0 || !slices.Equal(f.api.deleted, []int64{1, 2}) || f.drivers["pve"].egress {
		t.Fatalf("driver %+v, deleted %v", f.drivers["pve"], f.api.deleted)
	}
	f.group.Wait()
	f.tick(t) // sees the listener finished
	if !slices.Equal(f.sets.deleted, []contract.RepoName{repo}) {
		t.Fatalf("scale sets deleted: %v", f.sets.deleted)
	}
	f.drivers["pve"].listErr = errors.New("a quiet pool must not touch the host")
	f.tick(t)
}

func TestAListenerThatFailedAsItsRepositoryLeftStillDeletesTheSet(t *testing.T) {
	f := newFixture(1)
	f.sets.listenErr = errors.New("session lost")
	f.tick(t)
	f.group.Wait()
	f.store.settings = nil
	f.tick(t)
	if !slices.Equal(f.sets.deleted, []contract.RepoName{repo}) {
		t.Fatalf("scale sets deleted: %v", f.sets.deleted)
	}
}

// A host that hangs inside Tick would hold the lock, and with it --mode github.
func TestTickGivesUpOnAHostThatHangs(t *testing.T) {
	f := newFixture(1)
	f.tick(t)
	defer f.stop()
	if _, bounded := f.drivers["pve"].listCtx.Deadline(); !bounded {
		t.Fatal("a tick has no time limit")
	}
}

func TestDisablingWhileAHostIsDownKeepsLooking(t *testing.T) {
	f := newFixture(1)
	f.scale(1)
	f.store.settings = nil
	f.drivers["pve"].listErr = errors.New("ssh failed")
	if err := f.pool.Tick(context.Background(), &f.group); err == nil {
		t.Fatal("want the host failure")
	}
	f.drivers["pve"].listErr = nil
	f.tick(t)
	if len(f.drivers["pve"].runners) != 0 {
		t.Fatalf("runners %d", len(f.drivers["pve"].runners))
	}
}

func TestRestartAdoptsWorkingRunnersAndReapsTheRest(t *testing.T) {
	f := newFixture(2)
	f.drivers["pve"].runners = []box.Runner{{Name: "dyna-ci-pve-working", Repo: repo, ID: 7, Running: true}, {Name: "dyna-ci-pve-dead", Repo: repo, ID: 8}}
	f.drivers["pve"].egress = true
	f.api.list = []Registration{
		{ID: 7, Name: "dyna-ci-pve-working", Status: "offline"},
		{ID: 8, Name: "dyna-ci-pve-dead", Status: "offline"},
		{ID: 9, Name: "dyna-ci-pve-busy", Status: "offline", Busy: true},
		{ID: 10, Name: "dyna-ci-pve-online", Status: "online"},
		{ID: 11, Name: "someone-elses-runner", Status: "offline"},
	}
	f.tick(t)
	if !slices.Equal(f.drivers["pve"].removed, []string{"dyna-ci-pve-dead"}) {
		t.Fatalf("removed %v", f.drivers["pve"].removed)
	}
	if !slices.Equal(f.api.deleted, []int64{8}) {
		t.Fatalf("deleted registrations %v, want only the dead runner's", f.api.deleted)
	}
	f.scale(2)
	if len(f.sets.names) != 1 {
		t.Fatalf("registered %d, want one beside the adopted runner", len(f.sets.names))
	}
	f.stop()
}

func TestReapRunsHourly(t *testing.T) {
	f := newFixture(1)
	f.tick(t)
	f.now = f.now.Add(59 * time.Minute)
	f.tick(t)
	if f.api.listed != 1 {
		t.Fatalf("listed %d times within the hour", f.api.listed)
	}
	f.now = f.now.Add(2 * time.Minute)
	f.tick(t)
	if f.api.listed != 2 {
		t.Fatalf("listed %d times after the hour", f.api.listed)
	}
	f.stop()
}

func TestLeaseRenewsOnlyWhileThePoolServes(t *testing.T) {
	f := newFixture(1)
	f.tick(t)
	if len(f.api.leases) != 0 {
		t.Fatal("leased before any poll")
	}
	f.scale(0)
	f.tick(t)
	if len(f.api.leases) != 1 || !f.api.leases[0].Equal(f.now.Add(leaseFor)) {
		t.Fatalf("leases %v", f.api.leases)
	}
	f.now = f.now.Add(time.Minute)
	f.scale(0)
	f.tick(t)
	if len(f.api.leases) != 1 {
		t.Fatalf("renewed early: %v", f.api.leases)
	}
	f.now = f.now.Add(leaseEvery)
	f.scale(0)
	f.tick(t)
	if len(f.api.leases) != 2 {
		t.Fatalf("leases %v", f.api.leases)
	}
	// The listener died: polls stop, so the lease runs out.
	f.now = f.now.Add(serveWindow)
	f.tick(t)
	if len(f.api.leases) != 2 {
		t.Fatalf("a silent listener renewed its lease: %v", f.api.leases)
	}
	// Every host below its floor: polls go on, but with no capacity.
	f.store.hosts[0].DiskBytes = 0
	f.scale(0)
	f.tick(t)
	if len(f.api.leases) != 2 {
		t.Fatalf("a pool with no capacity renewed its lease: %v", f.api.leases)
	}
	f.stop()
}
