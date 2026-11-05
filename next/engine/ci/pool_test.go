package ci

import (
	"context"
	"errors"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

const repo contract.RepoName = "owner/repo"

const large = contract.BoxSizeLarge

type fakeStore struct {
	admission sync.Mutex
	repos     []store.CIRepo
	hosts     []box.Host
	boxes     []store.LifecycleBox
	pools     []store.Pool
	services  []store.ServiceRow
}

func (s *fakeStore) LockAdmission() func() {
	s.admission.Lock()
	return s.admission.Unlock
}

func (s *fakeStore) Services(context.Context, contract.RoomID) ([]store.ServiceRow, error) {
	return s.services, nil
}

func (s *fakeStore) CIRepos(context.Context) ([]store.CIRepo, error) { return s.repos, nil }
func (s *fakeStore) Hosts(context.Context) ([]box.Host, error)       { return s.hosts, nil }
func (s *fakeStore) LifecycleBoxes(context.Context) ([]store.LifecycleBox, error) {
	return s.boxes, nil
}
func (s *fakeStore) Pools(context.Context) ([]store.Pool, error) { return s.pools, nil }

type fakeDriver struct {
	runners     []box.Runner
	egress      bool
	builds      int
	specs       []box.RunnerSpec
	removed     []string
	startErr    error
	listErr     error
	listCtx     context.Context
	gate        chan struct{} // when set, a build waits for it to close
	buildMu     sync.Mutex    // a build-ahead runs beside placement
	onEgress    func()        // runs when the proxy is started, which a sweep does
	layerBuilds []string      // the id of each room layer built here
	failLayer   error         // when set, building a room layer fails with it
	hangLayer   bool          // when set, building a room layer waits for its context
	failShared  error         // when set, building the shared image fails with it
	pruned      [][]contract.RoomID
}

func (d *fakeDriver) BuildRunnerImage(ctx context.Context, layer *box.RunnerLayer) (string, error) {
	if d.gate != nil {
		select {
		case <-d.gate:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	d.buildMu.Lock()
	defer d.buildMu.Unlock()
	d.builds++
	if layer == nil && d.failShared != nil {
		return "", d.failShared
	}
	if layer != nil && d.hangLayer {
		d.layerBuilds = append(d.layerBuilds, layer.ID)
		d.buildMu.Unlock()
		<-ctx.Done()
		d.buildMu.Lock()
		return "", ctx.Err()
	}
	if layer != nil {
		d.layerBuilds = append(d.layerBuilds, layer.ID)
		if d.failLayer != nil {
			return "", d.failLayer
		}
		return "layer-" + layer.ID, nil
	}
	return "image", nil
}
func (d *fakeDriver) PruneRunnerLayers(_ context.Context, keep []contract.RoomID) error {
	d.pruned = append(d.pruned, keep)
	return nil
}
func (d *fakeDriver) EnsureEgress(context.Context, string) error {
	if d.onEgress != nil {
		d.onEgress()
	}
	d.egress = true
	return nil
}
func (d *fakeDriver) RemoveEgress(context.Context) error { d.egress = false; return nil }
func (d *fakeDriver) StartRunner(_ context.Context, s box.RunnerSpec) error {
	d.specs = append(d.specs, s)
	if d.startErr != nil {
		return d.startErr
	}
	d.runners = append(d.runners, box.Runner{Name: s.Name, Repo: s.Repo, ID: s.ID, Slot: s.Slot, Size: s.Size, Running: true})
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
	names     []string // runners registered
	sets      []string // the scale set of each
	jitErr    error
	listening map[string]int // starts of each repository's set, as "repo set"
	listenErr error
	deleted   []string
}

func (s *fakeSets) Listen(ctx context.Context, repo contract.RepoName, set string, _ Scale) error {
	s.mu.Lock()
	if s.listening == nil {
		s.listening = make(map[string]int)
	}
	s.listening[string(repo)+" "+set]++
	err := s.listenErr
	s.mu.Unlock()
	if err != nil {
		return err
	}
	<-ctx.Done()
	return nil
}
func (s *fakeSets) JIT(_ context.Context, _ contract.RepoName, set, name string) (int64, string, error) {
	if s.jitErr != nil {
		return 0, "", s.jitErr
	}
	s.next++
	s.names = append(s.names, name)
	s.sets = append(s.sets, set)
	return s.next, "jit-secret", nil
}
func (s *fakeSets) Delete(_ context.Context, repo contract.RepoName, set string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deleted = append(s.deleted, string(repo)+" "+set)
	return nil
}

type fixture struct {
	pool    *Pool
	store   *fakeStore
	drivers map[contract.HostName]*fakeDriver
	api     *fakeAPI
	sets    *fakeSets
	now     time.Time
	kicks   int
	group   sync.WaitGroup
}

func readyHost(name contract.HostName, memory int64) box.Host {
	return box.Host{Name: name, State: box.HostReady, CI: true, CPUs: 64, MemBytes: memory, DiskBytes: 100 * box.GiB, DiskFloor: 20 * box.GiB, CheckedAt: time.Unix(1, 0)}
}

// newFixture turns the repository on with max as its bound, beside one host with ci on.
func newFixture(max int) *fixture {
	f := &fixture{api: &fakeAPI{}, sets: &fakeSets{}, now: time.Unix(1_000_000, 0), drivers: map[contract.HostName]*fakeDriver{"pve": {}}}
	f.store = &fakeStore{repos: []store.CIRepo{{Repo: repo, Max: max, Room: "room"}}, hosts: []box.Host{readyHost("pve", 64*box.GiB)}}
	f.pool = &Pool{Store: f.store, GitHub: f.api, Sets: f.sets, Now: func() time.Time { return f.now }, Kick: func() { f.kicks++ },
		Hosts: func(_ context.Context, name contract.HostName) (Driver, error) { return f.drivers[name], nil }}
	return f
}

func (f *fixture) addHost(host box.Host) *fakeDriver {
	f.drivers[host.Name] = &fakeDriver{}
	f.store.hosts = append(f.store.hosts, host)
	return f.drivers[host.Name]
}

func (f *fixture) scaleSize(size contract.BoxSizeName, assigned int) int {
	return f.pool.scale(context.Background(), scaleSet{repo, size}, assigned)
}

func (f *fixture) scale(assigned int) int { return f.scaleSize(large, assigned) }

func (f *fixture) tick(t *testing.T) {
	t.Helper()
	f.tickNoWait(t)
	// A build-ahead is done before the test goes on, so placement sees the image as it would a poll later.
	for {
		f.pool.mu.Lock()
		busy := len(f.pool.building) > 0
		f.pool.mu.Unlock()
		if !busy {
			return
		}
		runtime.Gosched()
	}
}

func (f *fixture) tickNoWait(t *testing.T) {
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

func TestRunnersFollowAssignedJobsUpToTheBound(t *testing.T) {
	f := newFixture(2)
	if capacity := f.scale(0); capacity != 2 || len(f.sets.names) != 0 {
		t.Fatalf("idle pool: capacity %d, registered %v", capacity, f.sets.names)
	}
	f.scale(1)
	if len(f.drivers["pve"].runners) != 1 || !f.drivers["pve"].egress || f.drivers["pve"].builds != 1 {
		t.Fatalf("one job: %+v", f.drivers["pve"])
	}
	spec := f.drivers["pve"].specs[0]
	if spec.Config != "jit-secret" || spec.Repo != repo || spec.Size.Name != large || !strings.HasPrefix(spec.Name, "dyna-ci-pve-") {
		t.Fatalf("spec %+v", spec)
	}
	f.scale(1)
	if len(f.sets.names) != 1 {
		t.Fatalf("the same job started %d runners", len(f.sets.names))
	}
	f.scale(5)
	if len(f.drivers["pve"].runners) != 2 || f.drivers["pve"].builds != 1 {
		t.Fatalf("bound: %d runners, %d builds", len(f.drivers["pve"].runners), f.drivers["pve"].builds)
	}
}

// With no bound, admission alone decides: a 64-CPU host takes twice its CPUs of runners, sixteen large ones.
func TestWithoutABoundAdmissionDecides(t *testing.T) {
	f := newFixture(0)
	f.store.hosts[0].MemBytes = 256 * box.GiB // Leave memory ample so CPU sets the bound.
	if capacity := f.scale(20); capacity != 16 || len(f.drivers["pve"].runners) != 16 {
		t.Fatalf("capacity %d, runners %d", capacity, len(f.drivers["pve"].runners))
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

func TestAdmissionSpillsToTheNextHostWhenOneIsFull(t *testing.T) {
	f := newFixture(0)
	f.store.hosts[0].CPUs = 4 // equal CPU headroom, so the first runner takes the smaller name
	big := readyHost("big", 128*box.GiB)
	big.CPUs = 4 // one large runner
	f.addHost(big)
	f.scale(1)
	if len(f.drivers["big"].runners) != 1 || len(f.drivers["pve"].runners) != 0 {
		t.Fatalf("big %d, pve %d", len(f.drivers["big"].runners), len(f.drivers["pve"].runners))
	}
	f.scale(2)
	if len(f.drivers["pve"].runners) != 1 {
		t.Fatal("the second job did not spill to the other host")
	}
}

func TestATickRestoresTheProxyARunningJobNeeds(t *testing.T) {
	f := newFixture(1)
	defer f.stop()
	f.scale(1)
	d := f.drivers["pve"]
	d.egress = false // stopped, or the host restarted, so the restart policy did nothing
	f.tick(t)
	if !d.egress {
		t.Fatal("the running job was left with no proxy")
	}
}

func TestAnIdleHostGetsNoProxyUntilAJobNeedsIt(t *testing.T) {
	f := newFixture(1)
	defer f.stop()
	f.tick(t)
	if d := f.drivers["pve"]; d.egress { // the image builds ahead, the proxy waits
		t.Fatal("an idle pool started a proxy")
	}
}

func TestEachRunnerGetsItsOwnSlotAndAFinishedOneFreesIt(t *testing.T) {
	f := newFixture(3)
	f.scale(3)
	d := f.drivers["pve"]
	if got := []int{d.specs[0].Slot, d.specs[1].Slot, d.specs[2].Slot}; !slices.Equal(got, []int{0, 1, 2}) {
		t.Fatalf("slots %v", got)
	}
	d.runners = slices.DeleteFunc(d.runners, func(r box.Runner) bool { return r.Slot == 1 })
	f.scale(3)
	if slot := d.specs[3].Slot; slot != 1 {
		t.Fatalf("the replacement took slot %d, want the free one", slot)
	}
}

func TestAnotherRepositorysRunnerHoldsItsSlot(t *testing.T) {
	f := newFixture(1)
	d := f.drivers["pve"]
	other := contract.RepoName("owner/other")
	medium, _ := box.Size(contract.BoxSizeMedium)
	d.runners = []box.Runner{{Name: "dyna-ci-pve-other", Repo: other, ID: 9, Slot: 0, Size: medium, Running: true}}
	f.store.repos = append(f.store.repos, store.CIRepo{Repo: other, Room: "room"})
	f.scale(1)
	if slot := d.specs[0].Slot; slot != 1 {
		t.Fatalf("took slot %d, which another repository's runner holds", slot)
	}
}

func TestAHostWithEverySlotInUseStartsNoRunner(t *testing.T) {
	f := newFixture(0)
	f.store.hosts[0].CPUs, f.store.hosts[0].MemBytes = 128, 256*box.GiB
	if capacity := f.scale(box.RunnerSlots + 1); capacity != box.RunnerSlots {
		t.Fatalf("capacity %d", capacity)
	}
	if got := len(f.drivers["pve"].runners); got != box.RunnerSlots {
		t.Fatalf("%d runners on %d slots", got, box.RunnerSlots)
	}
}

func TestBelowTheDiskFloorCapacityFallsToWhatIsRunning(t *testing.T) {
	f := newFixture(3)
	size, _ := box.Size(large)
	f.drivers["pve"].runners = []box.Runner{{Name: "dyna-ci-pve-working", Repo: repo, ID: 7, Size: size, Running: true}}
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
	size, _ := box.Size(large)
	f.store.boxes = []store.LifecycleBox{{Box: box.Box{ID: "b", Host: "pve", State: contract.BoxReady, Image: "image", Size: contract.BoxSize{Name: size.Name, CPUs: 252, MemBytes: size.MemBytes}}}}
	f.scale(5)
	// 4 x 64 CPUs is 256, the box takes 252, and no large runner fits in the rest.
	if len(f.sets.names) != 0 {
		t.Fatalf("registered %v past the host's CPUs", f.sets.names)
	}
}

// A laptop drops off the tailnet mid-job. The pool stops seeing its runners and offers no capacity there. GitHub fails the
// job when the runner stops answering. The registration of an idle runner goes at the next reap, and that runner exits by
// itself when the host comes back (proved on a live runner).
func TestHostDroppingOutReapsItsIdleRunners(t *testing.T) {
	f := newFixture(0)
	f.store.hosts[0].DiskBytes = 0 // pve takes nothing, so both jobs go to the laptop
	laptop := f.addHost(readyHost("laptop", 256*box.GiB))
	f.scale(2)
	if len(laptop.runners) != 2 {
		t.Fatalf("laptop %d", len(laptop.runners))
	}
	idle, busy := laptop.runners[0], laptop.runners[1]
	f.tick(t)

	f.store.hosts[1].State = box.HostUnreachable
	if capacity := f.scale(0); capacity != 0 {
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
	// The host is back. Its idle runner has exited; its busy one still runs.
	f.store.hosts[1].State = box.HostReady
	laptop.runners = laptop.runners[1:]
	if capacity := f.scale(1); capacity != 16 || len(laptop.runners) != 1 {
		t.Fatalf("capacity %d, runners %d after the return", capacity, len(laptop.runners))
	}
	f.stop()
}

func TestHostThatStopsAnsweringIsSkippedForTheOthers(t *testing.T) {
	f := newFixture(0)
	f.addHost(readyHost("laptop", 128*box.GiB)).listErr = errors.New("ssh failed")
	if f.scale(1); len(f.drivers["pve"].runners) != 1 {
		t.Fatalf("pve %d", len(f.drivers["pve"].runners))
	}
}

func TestFailedStartDeletesRegistrationAndTriesAnotherHost(t *testing.T) {
	f := newFixture(0)
	big := f.addHost(readyHost("big", 128*box.GiB))
	big.startErr = errors.New("podman failed")
	f.scale(1)
	if !slices.Equal(f.api.deleted, []int64{1}) || len(f.drivers["pve"].runners) != 1 {
		t.Fatalf("deleted %v, pve %d", f.api.deleted, len(f.drivers["pve"].runners))
	}
	f.drivers["pve"].runners = nil
	f.scale(1)
	if len(big.specs) != 1 {
		t.Fatalf("retried the failed host inside the backoff: %d", len(big.specs))
	}
	big.startErr = nil
	f.drivers["pve"].runners = nil
	f.now = f.now.Add(retryAfter)
	delete(f.pool.images, imageKey{"pve", ""}) // pve, which has its image, would take the job before big, which must build again
	f.scale(1)
	if len(big.runners) != 1 || big.builds != 2 {
		t.Fatalf("big %+v", big)
	}
}

func TestFailedJITStartsNothingAndKeepsTheListener(t *testing.T) {
	f := newFixture(1)
	f.sets.jitErr = errors.New("service unavailable")
	if capacity := f.scale(1); capacity != 1 || len(f.drivers["pve"].specs) != 0 {
		t.Fatalf("capacity %d, specs %d", capacity, len(f.drivers["pve"].specs))
	}
}

func TestEachListenerRestartsAfterFailure(t *testing.T) {
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
	for _, size := range Sizes {
		if starts := f.sets.listening[string(repo)+" "+SetName(size)]; starts > 2 {
			t.Errorf("%s started %d listeners", SetName(size), starts)
		}
	}
	f.sets.mu.Unlock()
	f.stop()
}

func TestTurningOffRemovesRunnersAndEveryScaleSetThenGoesQuiet(t *testing.T) {
	f := newFixture(2)
	f.tick(t)
	f.scale(2)
	f.store.repos = nil
	f.tick(t) // cancels the listeners and removes the runners
	if len(f.drivers["pve"].runners) != 0 || !slices.Equal(f.api.deleted, []int64{1, 2}) || f.drivers["pve"].egress {
		t.Fatalf("driver %+v, deleted %v", f.drivers["pve"], f.api.deleted)
	}
	f.group.Wait()
	f.tick(t) // sees the listeners finished
	slices.Sort(f.sets.deleted)
	if want := []string{"owner/repo dyna-large", "owner/repo dyna-medium", "owner/repo dyna-small", "owner/repo dyna-xlarge"}; !slices.Equal(f.sets.deleted, want) {
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
	f.store.repos = nil
	f.tick(t)
	if len(f.sets.deleted) != len(Sizes) {
		t.Fatalf("scale sets deleted: %v", f.sets.deleted)
	}
}

// A host that hangs inside Tick would hold the lock, and with it --off.
func TestTickGivesUpOnAHostThatHangs(t *testing.T) {
	f := newFixture(1)
	f.tick(t)
	defer f.stop()
	if _, bounded := f.drivers["pve"].listCtx.Deadline(); !bounded {
		t.Fatal("a tick has no time limit")
	}
}

func TestTurningOffWhileAHostIsDownKeepsLooking(t *testing.T) {
	f := newFixture(1)
	f.scale(1)
	f.store.repos = nil
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
	size, _ := box.Size(large)
	f.drivers["pve"].runners = []box.Runner{{Name: "dyna-ci-pve-working", Repo: repo, ID: 7, Size: size, Running: true}, {Name: "dyna-ci-pve-dead", Repo: repo, ID: 8, Size: size}}
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

func TestLeaseRenewsEveryFiveMinutesWhileAHostIsPooled(t *testing.T) {
	f := newFixture(1)
	f.tick(t)
	if len(f.api.leases) != 1 || !f.api.leases[0].Equal(f.now.Add(leaseFor)) {
		t.Fatalf("leases %v", f.api.leases)
	}
	f.now = f.now.Add(leaseEvery - time.Second)
	f.tick(t)
	if len(f.api.leases) != 1 {
		t.Fatalf("renewed early: %v", f.api.leases)
	}
	f.now = f.now.Add(time.Second)
	f.tick(t)
	if len(f.api.leases) != 2 {
		t.Fatalf("leases %v", f.api.leases)
	}
	f.stop()
}

func TestLeaseRenewsOnTheFirstPassAfterARestart(t *testing.T) {
	f := newFixture(1)
	f.tick(t)
	f.stop()
	// A new engine knows nothing of the last write and has seen no poll.
	f.pool = &Pool{Store: f.store, GitHub: f.api, Sets: f.sets, Now: f.pool.Now, Hosts: f.pool.Hosts}
	f.now = f.now.Add(time.Minute)
	f.tick(t)
	if len(f.api.leases) != 2 || !f.api.leases[1].Equal(f.now.Add(leaseFor)) {
		t.Fatalf("leases %v", f.api.leases)
	}
	f.stop()
}

func TestLeaseRenewsWithEverySlotBusy(t *testing.T) {
	f := newFixture(1)
	// The bound is spent by a runner of another size, so this set's capacity is 0 though the pool is alive.
	small, _ := box.Size(contract.BoxSizeSmall)
	f.drivers["pve"].runners = []box.Runner{{Name: "dyna-ci-pve-1", Repo: repo, ID: 100, Slot: 0, Size: small, Running: true}}
	f.drivers["pve"].egress = true
	if capacity := f.scale(1); capacity != 0 {
		t.Fatalf("pool not saturated: capacity %d", capacity)
	}
	f.tick(t)
	f.now = f.now.Add(10 * time.Minute)
	f.scale(1)
	f.tick(t)
	if len(f.api.leases) != 2 {
		t.Fatalf("a full pool let its lease lapse: %v", f.api.leases)
	}
	f.stop()
}

func TestLeaseHoldsForAHostWithoutRoomButNotWithoutAHost(t *testing.T) {
	f := newFixture(1)
	// Below its disk floor the host takes no runner, and is still the pool.
	f.store.hosts[0].DiskBytes = 0
	f.tick(t)
	if len(f.api.leases) != 1 {
		t.Fatalf("leases %v", f.api.leases)
	}
	f.now = f.now.Add(leaseEvery)
	f.store.hosts[0].CI = false
	f.tick(t)
	if len(f.api.leases) != 1 {
		t.Fatalf("a host with ci off kept the lease: %v", f.api.leases)
	}
	f.store.hosts[0].CI = true
	f.drivers["pve"].listErr = errors.New("unreachable")
	f.pool.Tick(context.Background(), &f.group)
	if len(f.api.leases) != 1 {
		t.Fatalf("a host that did not answer kept the lease: %v", f.api.leases)
	}
	f.drivers["pve"].listErr = nil
	f.store.repos[0].Only = []string{"other"}
	f.tick(t)
	if len(f.api.leases) != 1 {
		t.Fatalf("a host the rule excludes kept the lease: %v", f.api.leases)
	}
	f.store.repos[0].Only = nil
	f.tick(t)
	if len(f.api.leases) != 2 {
		t.Fatalf("leases %v", f.api.leases)
	}
	f.stop()
}

func TestLeaseNeedsAListenerThatHasNotFailed(t *testing.T) {
	f := newFixture(1)
	f.tick(t)
	if len(f.api.leases) != 1 {
		t.Fatalf("leases %v", f.api.leases)
	}
	// Every listener ends and its replacement fails before any poll, as with a revoked token: the host still answers.
	f.stop()
	f.sets.listenErr = errors.New("unauthorized")
	f.now = f.now.Add(leaseEvery)
	f.pool.Tick(context.Background(), &f.group)
	f.group.Wait()
	f.now = f.now.Add(leaseEvery)
	f.pool.Tick(context.Background(), &f.group)
	f.group.Wait()
	if len(f.api.leases) != 1 {
		t.Fatalf("a pool nothing polls kept its lease: %v", f.api.leases)
	}
	// Once the listener polls again the lease returns.
	f.sets.listenErr = nil
	f.pool.Tick(context.Background(), &f.group)
	f.scale(0)
	f.now = f.now.Add(leaseEvery)
	f.tick(t)
	if len(f.api.leases) != 2 {
		t.Fatalf("leases %v", f.api.leases)
	}
	f.stop()
}

func TestLeaseIsWrittenBeforeTheSweep(t *testing.T) {
	smallSize, _ := box.Size(contract.BoxSizeSmall)
	f := newFixture(1)
	// A runner without its proxy makes the sweep start it, which on a slow host takes long.
	f.drivers["pve"].runners = []box.Runner{{Name: "dyna-ci-pve-1", Repo: repo, ID: 100, Slot: 0, Size: smallSize, Running: true}}
	var leasesAtSweep = -1
	f.drivers["pve"].onEgress = func() { leasesAtSweep = len(f.api.leases) }
	f.tick(t)
	if leasesAtSweep != 1 {
		t.Fatalf("the lease was written after the sweep: %d leases when it began", leasesAtSweep)
	}
	f.stop()
}

func TestNoHostsMeansNoLease(t *testing.T) {
	f := newFixture(1)
	f.store.hosts = nil
	f.tick(t)
	if len(f.api.leases) != 0 {
		t.Fatalf("leases %v", f.api.leases)
	}
	f.stop()
}

func TestLeaseFollowsTheRulesNamingOfAPool(t *testing.T) {
	f := newFixture(1)
	f.store.hosts[0].Pool = "burst"
	f.store.repos[0].Only = []string{"other"}
	f.tick(t)
	if len(f.api.leases) != 0 {
		t.Fatalf("a rule naming another pool kept the lease: %v", f.api.leases)
	}
	f.store.repos[0].Only = []string{"burst"}
	f.tick(t)
	if len(f.api.leases) != 1 {
		t.Fatalf("leases %v", f.api.leases)
	}
	f.stop()
}
