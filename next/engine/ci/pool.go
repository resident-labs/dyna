// Package ci serves a repository's GitHub Actions jobs from one runner scale set per size. A listener long-polls GitHub for
// each set, and each job it reports gets one ephemeral runner of that size, on a host that admission picks as it picks one
// for a box. See next/docs/ci-runners.md.
package ci

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

const (
	namePrefix = "dyna-ci-"

	// The lease is written for leaseFor every leaseEvery, so two writes in a row can fail, or a restart and a slow pass
	// can take their time, before a run sees the pool gone.
	leaseFor   = 15 * time.Minute
	leaseEvery = 5 * time.Minute
	reapEvery  = time.Hour
	retryAfter = 5 * time.Minute
	// scaleLimit is one pass, and leaves room for the first image build on a host.
	scaleLimit = 20 * time.Minute
)

// Sizes are the runner sizes a workflow can ask for, one scale set each. A session's agent size is too small for a job.
var Sizes = []contract.BoxSizeName{contract.BoxSizeSmall, contract.BoxSizeMedium, contract.BoxSizeLarge, contract.BoxSizeXLarge}

// SetName is a size's scale set, and the one label a workflow's runs-on names: dyna-small to dyna-xlarge.
func SetName(size contract.BoxSizeName) string { return "dyna-" + string(size) }

// Driver is what the pool needs from one host.
type Driver interface {
	BuildRunnerImage(context.Context, *box.RunnerLayer) (string, error)
	// PruneRunnerLayers removes the room layer images of every room but these.
	PruneRunnerLayers(context.Context, []contract.RoomID) error
	EnsureEgress(ctx context.Context, image string) error
	RemoveEgress(context.Context) error
	StartRunner(context.Context, box.RunnerSpec) error
	ListRunners(context.Context) ([]box.Runner, bool, error)
	RemoveRunner(context.Context, string) error
}

// API is the REST part of GitHub the pool calls.
type API interface {
	DeleteRunner(context.Context, contract.RepoName, int64) error
	ListRunners(context.Context, contract.RepoName) ([]Registration, error)
	RenewLease(context.Context, contract.RepoName, time.Time) error
}

// Scale receives the number of jobs assigned to a scale set and returns how many runners it can hold right now.
type Scale func(ctx context.Context, assigned int) (capacity int)

// Scaleset hides GitHub's scale set client, which is a preview API. set is a scale set's name.
type Scaleset interface {
	// Listen joins a repository's scale set, creating it when needed, and calls scale on every poll until ctx ends or the
	// session fails.
	Listen(ctx context.Context, repo contract.RepoName, set string, scale Scale) error
	// JIT returns a runner ID and its single-job config, which is a secret.
	JIT(ctx context.Context, repo contract.RepoName, set, name string) (int64, string, error)
	// Delete removes a repository's scale set.
	Delete(ctx context.Context, repo contract.RepoName, set string) error
}

// Store reads the settings and the placement inputs.
type Store interface {
	LockAdmission() func()
	CIRepos(context.Context) ([]store.CIRepo, error)
	Hosts(context.Context) ([]box.Host, error)
	LifecycleBoxes(context.Context) ([]store.LifecycleBox, error)
	Pools(context.Context) ([]store.Pool, error)
	Services(context.Context, contract.RoomID) ([]store.ServiceRow, error)
}

// Pool owns the listeners. Tick and every scale share mu, so one placement at a time sees the runners of the one before it.
// The fields from clean to reaped are guarded by mu. shown guards what the node pools read, since mu is held across SSH.
type Pool struct {
	Store  Store
	Hosts  func(context.Context, contract.HostName) (Driver, error)
	GitHub API
	Sets   Scaleset
	// Memory reads a host's latest memory use, as the box reconciler does.
	Memory func(contract.HostName) int64
	Health func(contract.HostName) contract.HostHealth
	Now    func() time.Time
	// Kick tells the node pools that the runners waiting for a node changed.
	Kick func()
	// LayerID reads the id of a room's ci-runner image, or "" when it has none, and is cheap. Layer reads the whole image, for a build.
	// Nil LayerID means no room has one. A room whose id cannot be read uses the shared image and shows a problem.
	LayerID func(contract.RoomID) (string, error)
	Layer   func(contract.RoomID) (*box.RunnerLayer, error)
	// LayerLimit is how long one layer may build, layerLimit when zero.
	LayerLimit time.Duration

	mu        sync.Mutex
	clean     bool // no repository wants runners and no host holds any
	listeners map[scaleSet]*listening
	images    map[imageKey]string          // each host's current runner images, once built or found there
	fellBack  map[imageKey]time.Time       // room layers whose build failed on a host, when, so its jobs run on the shared image
	failed    map[imageKey]time.Time       // builds that failed outright, when
	pruned    map[contract.HostName]string // the rooms whose layers each host kept at its last prune
	building  map[contract.HostName]bool   // hosts a build-ahead is working on
	retry     map[contract.HostName]time.Time
	leased    map[contract.RepoName]time.Time
	broken    map[scaleSet]bool // sets whose listener failed and has not polled since
	reaped    map[contract.RepoName]time.Time

	shown      sync.Mutex
	placed     []box.Box
	problems   map[contract.HostName]map[contract.RoomID]contract.RunnerImageProblem
	unreadable map[contract.RoomID]string // rooms whose ci-runner image could not be read, and why
	ciHosts    []contract.HostName
	waiting    map[scaleSet][]store.LifecycleBox
}

// scaleSet is one repository's scale set of one size.
type scaleSet struct {
	repo contract.RepoName
	size contract.BoxSizeName
}

type listening struct {
	// polled is false for a listener started to replace one that failed, until a poll reaches it. One that fails again
	// before any poll is not a pool that serves, and its restarts must not keep the lease.
	polled  bool
	cancel  context.CancelFunc
	done    chan struct{}
	err     error
	leaving bool
}

// Run keeps the listeners until ctx ends. Runners outlive the engine: a job in progress finishes, and the next listener
// counts its container.
func (p *Pool) Run(ctx context.Context, ticks <-chan time.Time) {
	var group sync.WaitGroup
	for {
		if err := p.Tick(ctx, &group); err != nil && ctx.Err() == nil {
			log.Printf("ci pool: %v", err)
		}
		select {
		case <-ctx.Done():
			group.Wait()
			return
		case <-ticks:
		}
	}
}

// Tick keeps a listener for each size of each repository that is on, and clears what the settings no longer want.
func (p *Pool) Tick(ctx context.Context, group *sync.WaitGroup) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	repos, err := p.Store.CIRepos(ctx)
	if err != nil {
		return err
	}
	wanted := make(map[scaleSet]bool)
	for _, repo := range repos {
		for _, size := range Sizes {
			wanted[scaleSet{repo.Repo, size}] = true
		}
	}
	failures := p.steer(ctx, wanted, group)
	if len(repos) == 0 && p.clean {
		return errors.Join(failures...)
	}
	// The lock is held across SSH, and --off acts here. A host that hangs must not leave a repository that cannot be turned off.
	// A build-ahead outlives this pass, so it takes the context Run gave, not the pass's timeout.
	buildCtx := ctx
	ctx, cancel := context.WithTimeout(ctx, scaleLimit)
	defer cancel()
	view, err := p.survey(ctx)
	if err != nil {
		return err
	}
	failures = append(failures, view.failures...)
	hosts := make([]box.Host, 0, len(view.hosts))
	for _, host := range view.hosts {
		hosts = append(hosts, host.host)
	}
	// The lease comes before anything that can take minutes (a build-ahead, a sweep over SSH), on a context of its own, so a
	// slow or hung host cannot make a due renewal late or fail it.
	for repo, row := range view.repos {
		if err := p.renew(buildCtx, repo, hosts, row.Only); err != nil {
			failures = append(failures, fmt.Errorf("repository %s: %w", repo, err))
		}
	}
	// What a layer no room has now left behind goes: its images, failures and problems, so a room that removed its image shows no
	// problem for good.
	current := make(map[string]bool)
	currentRooms := make(map[contract.RoomID]bool)
	for _, layer := range view.layers {
		current[layer.ID] = true
		currentRooms[layer.Room] = true
	}
	for _, table := range []map[imageKey]time.Time{p.fellBack, p.failed} {
		maps.DeleteFunc(table, func(key imageKey, _ time.Time) bool { return key.layer != "" && !current[key.layer] })
	}
	maps.DeleteFunc(p.images, func(key imageKey, _ string) bool { return key.layer != "" && !current[key.layer] })
	p.shown.Lock()
	for _, rooms := range p.problems {
		maps.DeleteFunc(rooms, func(room contract.RoomID, _ contract.RunnerImageProblem) bool { return !currentRooms[room] })
	}
	p.ciHosts = p.ciHosts[:0]
	for _, host := range view.hosts {
		if host.host.CI {
			p.ciHosts = append(p.ciHosts, host.host.Name)
		}
	}
	p.shown.Unlock()
	for _, host := range view.hosts {
		p.buildAhead(buildCtx, host, view.layers, group)
	}
	alive := make(map[contract.RepoName]map[string]bool)
	for _, host := range view.hosts {
		if err := p.sweep(ctx, host, view, alive); err != nil {
			failures = append(failures, fmt.Errorf("host %s: %w", host.host.Name, err))
		}
	}
	for repo := range view.repos {
		if err := p.upkeep(ctx, repo, alive[repo]); err != nil {
			failures = append(failures, fmt.Errorf("repository %s: %w", repo, err))
		}
	}
	// One pass after the last repository leaves removes its runners. A pass that failed looks again, and the first pass
	// reaps what an earlier engine left.
	p.clean = len(repos) == 0 && len(failures) == 0
	return errors.Join(failures...)
}

// steer starts missing listeners, restarts failed ones on the next tick, and deletes the scale sets of a repository that left.
func (p *Pool) steer(ctx context.Context, wanted map[scaleSet]bool, group *sync.WaitGroup) []error {
	var failures []error
	if p.listeners == nil {
		p.listeners = make(map[scaleSet]*listening)
	}
	for key, l := range p.listeners {
		finished := false
		select {
		case <-l.done:
			finished = true
		default:
		}
		switch {
		case !finished && !wanted[key]:
			l.cancel()
			l.leaving = true
		case finished && (l.leaving || !wanted[key]):
			// A listener that failed just as its repository left still owes the delete.
			if err := p.Sets.Delete(ctx, key.repo, SetName(key.size)); err != nil {
				failures = append(failures, fmt.Errorf("repository %s: delete scale set %s: %w", key.repo, SetName(key.size), err))
				continue
			}
			p.wait(key, nil)
			delete(p.listeners, key)
		case finished:
			if ctx.Err() == nil {
				failures = append(failures, fmt.Errorf("repository %s: listener %s stopped: %w", key.repo, SetName(key.size), l.err))
			}
			delete(p.listeners, key)
			if p.broken == nil {
				p.broken = make(map[scaleSet]bool)
			}
			p.broken[key] = true
		}
	}
	for key := range wanted {
		if p.listeners[key] != nil || ctx.Err() != nil {
			continue
		}
		listenCtx, cancel := context.WithCancel(ctx)
		l := &listening{cancel: cancel, done: make(chan struct{}), polled: !p.broken[key]}
		p.listeners[key] = l
		group.Add(1)
		go func() {
			defer group.Done()
			defer close(l.done)
			defer cancel()
			l.err = p.Sets.Listen(listenCtx, key.repo, SetName(key.size), func(scaleCtx context.Context, assigned int) int {
				return p.scale(scaleCtx, key, assigned)
			})
		}()
	}
	return failures
}

// scale runs on every long poll, so it also retries whatever the last pass could not do. It never fails: a host problem must
// not end the listener.
func (p *Pool) scale(ctx context.Context, key scaleSet, assigned int) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	if ctx.Err() != nil {
		return 0
	}
	// The lock is held across SSH, so a host that hangs cannot stall the others for long.
	ctx, cancel := context.WithTimeout(ctx, scaleLimit)
	defer cancel()
	view, err := p.survey(ctx)
	if err != nil {
		log.Printf("ci pool: %v", err)
		return 0
	}
	for _, failure := range view.failures {
		log.Printf("ci pool: %v", failure)
	}
	if l := p.listeners[key]; l != nil {
		l.polled = true
	}
	delete(p.broken, key)
	capacity, err := p.place(ctx, key, assigned, view)
	if err != nil {
		log.Printf("ci pool: repository %s: %s: %v", key.repo, SetName(key.size), err)
	}
	return capacity
}

// survey is what one pass knows about the fleet. Only ready hosts that answered are in hosts.
type survey struct {
	repos    map[contract.RepoName]store.CIRepo
	layers   map[contract.RepoName]*box.RunnerLayer // the ci-runner image of each repository's room, where it has one
	hosts    map[contract.HostName]*surveyed
	boxes    []box.Box
	rows     []store.LifecycleBox
	services []store.ServiceRow
	pools    []store.Pool
	nodes    map[string]int // each pool's nodes
	failures []error
}

type surveyed struct {
	host    box.Host
	driver  Driver
	runners []box.Runner
	egress  bool
}

func (p *Pool) survey(ctx context.Context) (survey, error) {
	s := survey{repos: make(map[contract.RepoName]store.CIRepo), hosts: make(map[contract.HostName]*surveyed), nodes: make(map[string]int)}
	repos, err := p.Store.CIRepos(ctx)
	if err != nil {
		return s, err
	}
	unreadable := make(map[contract.RoomID]string)
	if p.LayerID != nil {
		s.layers = make(map[contract.RepoName]*box.RunnerLayer)
	}
	for _, repo := range repos {
		s.repos[repo.Repo] = repo
		if p.LayerID != nil {
			// One room's unreadable image is that room's problem. The pool goes on, and its jobs use the shared image.
			id, err := p.LayerID(repo.Room)
			switch {
			case err != nil:
				unreadable[repo.Room] = err.Error()
				p.shown.Lock()
				changed := p.unreadable[repo.Room] != err.Error()
				p.shown.Unlock()
				if changed {
					log.Printf("ci pool: room %s: read its ci-runner image: %v. Its jobs use the shared image.", repo.Room, err)
				}
			case id != "":
				s.layers[repo.Repo] = &box.RunnerLayer{Room: repo.Room, ID: id}
			}
		}
	}
	if p.LayerID != nil {
		p.shown.Lock()
		p.unreadable = unreadable
		p.shown.Unlock()
	}
	hosts, err := p.Store.Hosts(ctx)
	if err != nil {
		return s, err
	}
	if s.rows, err = p.Store.LifecycleBoxes(ctx); err != nil {
		return s, err
	}
	if s.services, err = p.Store.Services(ctx, ""); err != nil {
		return s, err
	}
	for _, row := range s.rows {
		s.boxes = append(s.boxes, row.Box)
	}
	if s.pools, err = p.Store.Pools(ctx); err != nil {
		return s, err
	}
	var unanswered []contract.HostName
	for _, host := range hosts {
		if host.Pool != "" {
			s.nodes[host.Pool]++
		}
		if p.Health != nil {
			host.Health = p.Health(host.Name)
		}
		if host.State != box.HostReady {
			continue
		}
		if p.Memory != nil {
			host.MemUsed = p.Memory(host.Name)
		}
		driver, err := p.Hosts(ctx, host.Name)
		var runners []box.Runner
		var egress bool
		if err == nil {
			runners, egress, err = driver.ListRunners(ctx)
		}
		if err != nil {
			s.failures = append(s.failures, fmt.Errorf("host %s: %w", host.Name, err))
			unanswered = append(unanswered, host.Name)
			continue
		}
		s.hosts[host.Name] = &surveyed{host: host, driver: driver, runners: runners, egress: egress}
	}
	// A host that did not answer still holds what it held, as far as the node pools know. Publishing a new runner snapshot is
	// serialized with service and box admission, just as claiming a runner is.
	unlock := p.Store.LockAdmission()
	defer unlock()
	p.shown.Lock()
	placed := slices.DeleteFunc(p.placed, func(b box.Box) bool { return !slices.Contains(unanswered, b.Host) })
	for _, host := range s.hosts {
		for _, runner := range host.runners {
			if runner.Running {
				placed = append(placed, runnerBox(runner.Name, host.host.Name, runner.Size))
			}
		}
	}
	p.placed = placed
	p.shown.Unlock()
	return s, nil
}

// runnerBox makes a runner visible to admission. It has an image, so admission counts its CPUs and memory but no disk: all
// it writes goes to tmpfs, or to its build folder in the host's cache.
func runnerBox(name string, host contract.HostName, size contract.BoxSize) box.Box {
	return box.Box{ID: contract.BoxID(name), Host: host, Image: box.RunnerImage, Size: size, State: contract.BoxReady}
}

// runnerRequest is a runner as admission sees it before it has a host, and as the node pools see one that waits for a node.
func runnerRequest(id contract.BoxID, room contract.RoomID, size contract.BoxSize) box.Box {
	return box.Box{ID: id, Room: room, Image: box.RunnerImage, Size: size, State: contract.BoxPending, Reason: string(box.PendingPoolNode)}
}

// waitID names a job that has no runner yet by its place in its scale set's queue. It stays the same from one poll to the
// next, so a per-session node made for it takes its runner, and no second node is made for it.
func waitID(key scaleSet, job int) contract.BoxID {
	return contract.BoxID(fmt.Sprintf("%swait-%s-%s-%d", namePrefix, strings.ReplaceAll(string(key.repo), "/", "-"), key.size, job))
}

// place starts a runner for each assigned job that has none, on a ready host with ci on where no session box waits for room,
// as admission picks one for a box. A
// job that no host takes waits for a node of its room's pool, as a box does. It returns the capacity: the runners of this
// size, the ones admission would still take, and the nodes the room's pools could add, within the repository's bound.
func (p *Pool) place(ctx context.Context, key scaleSet, assigned int, view survey) (int, error) {
	repo, on := view.repos[key.repo]
	if !on {
		p.wait(key, nil)
		return 0, nil
	}
	size, _ := box.Size(key.size)
	boxes := slices.Clone(view.boxes)
	held := make(map[contract.HostName]int) // each host's running runners, which hold its slots
	running, others := 0, 0
	for _, host := range view.hosts {
		for _, runner := range host.runners {
			if !runner.Running {
				continue
			}
			boxes = append(boxes, runnerBox(runner.Name, host.host.Name, runner.Size))
			held[host.host.Name]++
			switch {
			case runner.Repo != key.repo:
			case runner.Size.Name == key.size:
				running++
			default:
				others++
			}
		}
	}
	allowed := len(view.hosts) * box.RunnerSlots
	if repo.Max > 0 {
		allowed = repo.Max - others
	}
	hosts := make([]box.Host, 0, len(view.hosts))
	for _, host := range view.hosts {
		hosts = append(hosts, host.host)
	}
	admit := func(id contract.BoxID) contract.HostName {
		var open []box.Host
		for _, host := range box.RunnerHosts(hosts) {
			if held[host.Name] < box.RunnerSlots && !p.Now().Before(p.retry[host.Name]) && !waitsForRoom(view.hosts[host.Name].host, view) {
				open = append(open, host)
			}
		}
		// A host that has the image starts a runner at once, and one that must build it makes the job wait minutes, so a host
		// with the image takes the runner while it has room.
		ready := slices.DeleteFunc(slices.Clone(open), func(h box.Host) bool { return p.imageFor(h.Name, view.layers[key.repo]) == "" })
		if host := box.Admit(ready, boxes, runnerRequest(id, repo.Room, size), repo.Only...).Host; host != "" {
			return host
		}
		return box.Admit(open, boxes, runnerRequest(id, repo.Room, size), repo.Only...).Host
	}
	var failures []error
	var waiting []store.LifecycleBox
	for job := running; job < min(assigned, allowed); {
		id := waitID(key, job-running)
		host := admit(id)
		if host == "" {
			waiting = append(waiting, store.LifecycleBox{Box: runnerRequest(id, repo.Room, size), Want: store.WantRun, Only: repo.Only})
			job++
			continue
		}
		if p.building[host] {
			// Its image is being built ahead. A second build would race the first, so the job takes a later poll.
			break
		}
		name, err := p.start(ctx, key, view.hosts[host], size, view.layers[key.repo])
		var registration jitError
		if errors.As(err, &registration) {
			// GitHub, not the host, failed, and the next poll tries again.
			failures = append(failures, err)
			break
		}
		if err != nil {
			failures = append(failures, fmt.Errorf("host %s: %w", host, err))
			if p.retry == nil {
				p.retry = make(map[contract.HostName]time.Time)
			}
			p.retry[host] = p.Now().Add(retryAfter)
			continue
		}
		boxes = append(boxes, runnerBox(name, host, size))
		held[host]++
		running++
		job++
	}
	p.wait(key, waiting)
	// The rest is only imagined: GitHub assigns that many more jobs, and the next poll starts their runners.
	capacity := running
	for ; capacity < allowed; capacity++ {
		host := admit(namePrefix + "spare")
		if host == "" {
			break
		}
		boxes = append(boxes, runnerBox(fmt.Sprintf("%sspare-%d", namePrefix, capacity), host, size))
		held[host]++
	}
	for _, pool := range view.pools {
		if slices.Contains(repo.Only, pool.Name) {
			capacity += max(pool.Max-view.nodes[pool.Name], 0)
		}
	}
	return max(min(capacity, allowed), 0), errors.Join(failures...)
}

// start prepares a host and starts one runner on it.
func (p *Pool) start(ctx context.Context, key scaleSet, host *surveyed, size contract.BoxSize, layer *box.RunnerLayer) (string, error) {
	image, err := p.prepare(ctx, host, layer)
	if err != nil {
		return "", err
	}
	slot, err := freeSlot(host.runners)
	if err != nil {
		return "", err
	}
	var raw [4]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	name := namePrefix + string(host.host.Name) + "-" + hex.EncodeToString(raw[:])
	if err := p.claim(ctx, runnerBox(name, host.host.Name, size), host.host.Box); err != nil {
		return "", err
	}
	id, config, err := p.Sets.JIT(ctx, key.repo, SetName(key.size), name)
	if err != nil {
		return "", jitError{err}
	}
	if err := host.driver.StartRunner(ctx, box.RunnerSpec{Name: name, Image: image, Config: config, Repo: key.repo, ID: id, Slot: slot, Size: size}); err != nil {
		// The next attempt checks the image again, in case a host lost it.
		delete(p.images, imageKey{host.host.Name, layerID(layer)})
		// A runner that did start after an uncertain reply loses its registration and exits.
		return "", errors.Join(err, p.GitHub.DeleteRunner(ctx, key.repo, id))
	}
	host.runners = append(host.runners, box.Runner{Name: name, Repo: key.repo, ID: id, Slot: slot, Size: size, Running: true})
	return name, nil
}

// jitError is a failed registration: GitHub's, not the host's.
type jitError struct{ error }

func (e jitError) Unwrap() error { return e.error }

// imageKey names one runner image on one host: the shared image when layer is empty, else the room's layer with that id.
type imageKey struct {
	host  contract.HostName
	layer string
}

func layerID(layer *box.RunnerLayer) string {
	if layer == nil {
		return ""
	}
	return layer.ID
}

// imageFor returns the image a runner of this layer would start on this host now, or "" when none is ready: the layer's own,
// or the shared image where the layer's build failed.
func (p *Pool) imageFor(host contract.HostName, layer *box.RunnerLayer) string {
	if image := p.images[imageKey{host, layerID(layer)}]; image != "" {
		return image
	}
	if _, failed := p.fellBack[imageKey{host, layerID(layer)}]; failed {
		return p.images[imageKey{host, ""}]
	}
	return ""
}

// layerLimit bounds one room's layer build on a host, so one hanging Containerfile cannot use the pass's budget, which every
// other room's layer on that host shares. A longer build counts as a failed one.
const layerLimit = 10 * time.Minute

// built is the outcome of building a layer on a host. Image is what its runners use. Reason is why the layer failed, when
// they use the shared image instead.
type built struct {
	image, reason string
}

// build builds the shared image on a host, then a layer's image on it. A layer that fails to load, to build or to finish within
// its limit is a failure of that layer alone: its runners use the shared image and Reason says why, with the build's last
// lines. A shared image that fails to build, or a pass that was cancelled, is an error. It touches no pool state, so a caller
// may hold p.mu or not.
func (p *Pool) build(ctx context.Context, driver Driver, ref *box.RunnerLayer) (built, error) {
	shared, err := driver.BuildRunnerImage(ctx, nil)
	if err != nil || ref == nil {
		return built{image: shared}, err
	}
	limit := p.LayerLimit
	if limit == 0 {
		limit = layerLimit
	}
	layerCtx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	// The context is read now, from the room's current save, and only for a build that is needed.
	layer, err := p.Layer(ref.Room)
	if err == nil && (layer == nil || layer.ID != ref.ID) {
		err = errors.New("the room saved another image meanwhile; the next pass builds it")
	}
	if err == nil {
		var image string
		if image, err = driver.BuildRunnerImage(layerCtx, layer); err == nil {
			return built{image: image}, nil
		}
	}
	if ctx.Err() != nil {
		return built{}, ctx.Err()
	}
	reason := err.Error()
	var logged interface{ BuildLog() string }
	if errors.Is(layerCtx.Err(), context.DeadlineExceeded) {
		reason = fmt.Sprintf("the build took longer than %s", limit)
	} else if errors.As(err, &logged) {
		reason += ": " + logged.BuildLog()
	}
	return built{image: shared, reason: reason}, nil
}

// maxReason is how much of a build's output a problem keeps: its end, where the error is.
const maxReason = 400

// remember records a build's outcome. The caller holds p.mu.
func (p *Pool) remember(host contract.HostName, layer *box.RunnerLayer, result built) {
	if p.images == nil {
		p.images = make(map[imageKey]string)
	}
	if p.fellBack == nil {
		p.fellBack = make(map[imageKey]time.Time)
	}
	key := imageKey{host, layerID(layer)}
	p.shown.Lock()
	defer p.shown.Unlock()
	if layer != nil && result.reason != "" {
		reason := strings.Join(strings.Fields(result.reason), " ")
		if runes := []rune(reason); len(runes) > maxReason {
			reason = "…" + string(runes[len(runes)-maxReason:])
		}
		p.images[imageKey{host, ""}] = result.image
		p.fellBack[key] = p.Now()
		if p.problems == nil {
			p.problems = make(map[contract.HostName]map[contract.RoomID]contract.RunnerImageProblem)
		}
		if p.problems[host] == nil {
			p.problems[host] = make(map[contract.RoomID]contract.RunnerImageProblem)
		}
		p.problems[host][layer.Room] = contract.RunnerImageProblem{Room: layer.Room, Image: layer.ID, Reason: reason}
		log.Printf("ci pool: host %s: build the runner image of room %s: %s. Its jobs use the shared image.", host, layer.Room, reason)
		return
	}
	p.images[key] = result.image
	delete(p.fellBack, key)
	delete(p.failed, key)
	if layer != nil {
		delete(p.problems[host], layer.Room)
	}
}

// Problems lists the runner images that failed to build on each host, for host list, and a room whose image could not be read
// on every ci host.
func (p *Pool) Problems() map[contract.HostName][]contract.RunnerImageProblem {
	p.shown.Lock()
	defer p.shown.Unlock()
	out := make(map[contract.HostName][]contract.RunnerImageProblem, len(p.problems))
	for host, rooms := range p.problems {
		for _, problem := range rooms {
			out[host] = append(out[host], problem)
		}
	}
	for _, host := range p.ciHosts {
		for room, reason := range p.unreadable {
			out[host] = append(out[host], contract.RunnerImageProblem{Room: room, Reason: "the image could not be read: " + reason})
		}
	}
	for _, problems := range out {
		slices.SortFunc(problems, func(a, b contract.RunnerImageProblem) int { return strings.Compare(string(a.Room), string(b.Room)) })
	}
	return out
}

// prepare returns a host's runner image for a layer, with its proxy running.
func (p *Pool) prepare(ctx context.Context, host *surveyed, layer *box.RunnerLayer) (string, error) {
	image := p.imageFor(host.host.Name, layer)
	if image == "" {
		result, err := p.build(ctx, host.driver, layer)
		if err != nil {
			return "", err
		}
		p.remember(host.host.Name, layer, result)
		image = result.image
	}
	if !host.egress {
		if err := host.driver.EnsureEgress(ctx, image); err != nil {
			return "", err
		}
		host.egress = true
	}
	return image, nil
}

// buildAhead builds a host's runner images in the background when the pool has not seen them there, as after a deploy, on a new
// host, or when a room saves a new ci-runner image: the shared image, then each room's layer, from the engine's copy at this
// moment, so a host that was offline for a save builds the current image and never an old one. A build that failed is tried
// again after retryAfter, and each layer has a time limit of its own. It also removes the layers of rooms that have none now.
// It does not hold p.mu while it builds, so placement goes on, and place prefers hosts whose image is ready. A build reuses an
// image the host already has, so a host that has them is ready within moments.
func (p *Pool) buildAhead(ctx context.Context, host *surveyed, layers map[contract.RepoName]*box.RunnerLayer, group *sync.WaitGroup) {
	name := host.host.Name
	if !host.host.CI || host.host.Health == contract.HostDegraded || p.building[name] {
		return
	}
	var want []*box.RunnerLayer
	waiting := func(key imageKey) bool {
		at, failed := p.failed[key]
		if !failed {
			at, failed = p.fellBack[key]
		}
		return failed && p.Now().Before(at.Add(retryAfter))
	}
	if p.images[imageKey{name, ""}] == "" && !waiting(imageKey{name, ""}) {
		want = append(want, nil)
	}
	seen := make(map[string]bool)
	var rooms []contract.RoomID
	for _, layer := range layers {
		rooms = append(rooms, layer.Room)
		key := imageKey{name, layer.ID}
		if seen[layer.ID] || p.images[key] != "" || waiting(key) {
			continue
		}
		seen[layer.ID] = true
		want = append(want, layer)
	}
	slices.Sort(rooms)
	rooms = slices.Compact(rooms)
	pruneKey := fmt.Sprint(rooms)
	pruned, prunedBefore := p.pruned[name]
	prune := !prunedBefore || pruned != pruneKey
	if len(want) == 0 && !prune {
		return
	}
	if p.building == nil {
		p.building = make(map[contract.HostName]bool)
	}
	p.building[name] = true
	group.Add(1)
	go func() {
		defer group.Done()
		buildCtx, cancel := context.WithTimeout(ctx, scaleLimit)
		defer cancel()
		results := make([]built, len(want))
		errs := make([]error, len(want))
		for i, layer := range want {
			results[i], errs[i] = p.build(buildCtx, host.driver, layer)
		}
		var pruneErr error
		if prune {
			pruneErr = host.driver.PruneRunnerLayers(buildCtx, rooms)
		}
		p.mu.Lock()
		defer p.mu.Unlock()
		delete(p.building, name)
		if prune && pruneErr == nil {
			if p.pruned == nil {
				p.pruned = make(map[contract.HostName]string)
			}
			p.pruned[name] = pruneKey
		}
		if pruneErr != nil {
			log.Printf("ci pool: host %s: remove old runner images: %v", name, pruneErr)
		}
		for i, layer := range want {
			if errs[i] != nil {
				log.Printf("ci pool: host %s: build runner image: %v", name, errs[i])
				if p.failed == nil {
					p.failed = make(map[imageKey]time.Time)
				}
				p.failed[imageKey{name, layerID(layer)}] = p.Now()
				continue
			}
			p.remember(name, layer, results[i])
		}
	}()
}

// freeSlot picks the slot of a new runner on a host: its proxy socket and its build folder. Every repository's runners count,
// because they share that host's proxy and cache. A slot a dead runner used is free: nothing of it outlives its container.
func freeSlot(runners []box.Runner) (int, error) {
	used := make(map[int]bool)
	for _, runner := range runners {
		used[runner.Slot] = used[runner.Slot] || runner.Running
	}
	for slot := range box.RunnerSlots {
		if !used[slot] {
			return slot, nil
		}
	}
	return 0, errors.New("every slot on this host is in use")
}

// sweep removes runners nothing wants, and the proxy once a host needs none.
func (p *Pool) sweep(ctx context.Context, host *surveyed, view survey, alive map[contract.RepoName]map[string]bool) error {
	serving := false
	for _, runner := range host.runners {
		if _, on := view.repos[runner.Repo]; !on {
			// Turning a repository off stops its runners now, including one mid-job.
			if err := host.driver.RemoveRunner(ctx, runner.Name); err != nil {
				return err
			}
			if err := p.GitHub.DeleteRunner(ctx, runner.Repo, runner.ID); err != nil {
				return err
			}
			continue
		}
		if !runner.Running {
			if err := host.driver.RemoveRunner(ctx, runner.Name); err != nil {
				return err
			}
			continue
		}
		if alive[runner.Repo] == nil {
			alive[runner.Repo] = make(map[string]bool)
		}
		alive[runner.Repo][runner.Name] = true
		serving = true
	}
	// A host with ci off keeps its proxy while its runners finish their jobs.
	if !serving && host.egress && (!host.host.CI || len(view.repos) == 0) {
		return host.driver.RemoveEgress(ctx)
	}
	// Its restart policy covers a crash, not a stop or a host restart, and a runner with no proxy loses its job.
	if serving && !host.egress {
		_, err := p.prepare(ctx, host, nil)
		return err
	}
	return nil
}

// leaseWrite bounds one lease write.
const leaseWrite = 30 * time.Second

// renew writes the lease while the pool is alive for the repository: a listener of it runs and has not failed since its last
// poll, and a host that is ready, has
// ci on and is allowed by the room's rule answered this pass's survey. Free slots, the bound, disk, memory and the polls'
// capacity do not count, so a full pool keeps its lease. Only the time of the last write is kept in memory, so the first pass
// after a start writes at once.
func (p *Pool) renew(ctx context.Context, repo contract.RepoName, hosts []box.Host, only []string) error {
	now := p.Now()
	if now.Before(p.leased[repo]) {
		return nil
	}
	listening := false
	for key, l := range p.listeners {
		if key.repo != repo {
			continue
		}
		select {
		case <-l.done:
		default:
			listening = listening || l.polled
		}
	}
	if !listening || !slices.ContainsFunc(hosts, func(h box.Host) bool {
		// The room's rule names hosts, or the pool of a node; a per-session node serves its one box only.
		name := string(h.Name)
		if h.Pool != "" {
			name = h.Pool
		}
		return h.CI && h.Box == "" && (len(only) == 0 || slices.Contains(only, name))
	}) {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, leaseWrite)
	defer cancel()
	if err := p.GitHub.RenewLease(ctx, repo, now.Add(leaseFor)); err != nil {
		return err
	}
	if p.leased == nil {
		p.leased = make(map[contract.RepoName]time.Time)
	}
	p.leased[repo] = now.Add(leaseEvery)
	return nil
}

// upkeep removes dead registrations.
func (p *Pool) upkeep(ctx context.Context, repo contract.RepoName, alive map[string]bool) error {
	var failures []error
	now := p.Now()
	if !now.Before(p.reaped[repo]) {
		if err := p.reap(ctx, repo, alive); err != nil {
			failures = append(failures, err)
		} else {
			if p.reaped == nil {
				p.reaped = make(map[contract.RepoName]time.Time)
			}
			p.reaped[repo] = now.Add(reapEvery)
		}
	}
	return errors.Join(failures...)
}

// reap removes registrations no container answers for: a runner killed before it finished a job, or one on a host that
// dropped off the network. A runner whose registration goes exits by itself when its host returns.
func (p *Pool) reap(ctx context.Context, repo contract.RepoName, alive map[string]bool) error {
	registered, err := p.GitHub.ListRunners(ctx, repo)
	if err != nil {
		return err
	}
	for _, runner := range registered {
		if !strings.HasPrefix(runner.Name, namePrefix) || runner.Status != "offline" || runner.Busy || alive[runner.Name] {
			continue
		}
		if err := p.GitHub.DeleteRunner(ctx, repo, runner.ID); err != nil {
			return err
		}
	}
	return nil
}

// waitsForRoom says whether a session box or a service that waits for CPU or memory would take the host if its runners were gone. Such a
// box comes before a runner: a runner started in each slot that frees up would keep it waiting. A box that fits nowhere, that
// its room's rule keeps off the host, or that other boxes keep out is not waiting on runners and holds none back. The
// snapshot holds no runners, so Admit sees the host as it is without them.
func waitsForRoom(h box.Host, view survey) bool {
	for _, row := range view.services {
		if row.State != contract.ServicePending || (row.Reason != contract.ServiceNoCPU && row.Reason != contract.ServiceNoMemory) {
			continue
		}
		if row.Host != "" && row.Host != h.Name {
			continue
		}
		if box.AdmitService([]box.Host{h}, view.boxes, box.ServiceRequest{ID: row.ID, Room: row.Room, Parts: row.Reservation(), ImageEstimate: max(row.ImageBytes, row.ImageEstimate)}, row.Only...).Host == h.Name {
			return true
		}
	}
	for _, row := range view.rows {
		if row.State != contract.BoxPending || row.Image == box.RunnerImage || (row.Reason != string(box.PendingCPU) && row.Reason != string(box.PendingMemory)) {
			continue
		}
		if row.Host != "" && row.Host != h.Name {
			continue
		}
		if box.Admit([]box.Host{h}, view.boxes, row.Box, row.Only...).Host == h.Name {
			return true
		}
	}
	return false
}
