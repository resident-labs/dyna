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

	leaseFor   = 10 * time.Minute
	leaseEvery = 5 * time.Minute
	// serveWindow is how old the last long poll may be for the pool to count as
	// alive. A poll returns within about a minute.
	serveWindow = 3 * time.Minute
	reapEvery   = time.Hour
	retryAfter  = 5 * time.Minute
	// scaleLimit is one pass, and leaves room for the first image build on a host.
	scaleLimit = 20 * time.Minute
)

// Sizes are the runner sizes a workflow can ask for, one scale set each. A session's agent size is too small for a job.
var Sizes = []contract.BoxSizeName{contract.BoxSizeSmall, contract.BoxSizeMedium, contract.BoxSizeLarge, contract.BoxSizeXLarge}

// SetName is a size's scale set, and the one label a workflow's runs-on names: dyna-small to dyna-xlarge.
func SetName(size contract.BoxSizeName) string { return "dyna-" + string(size) }

// Driver is what the pool needs from one host.
type Driver interface {
	BuildRunnerImage(context.Context) (string, error)
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
	CIRepos(context.Context) ([]store.CIRepo, error)
	Hosts(context.Context) ([]box.Host, error)
	LifecycleBoxes(context.Context) ([]store.LifecycleBox, error)
	Pools(context.Context) ([]store.Pool, error)
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
	Now    func() time.Time
	// Kick tells the node pools that the runners waiting for a node changed.
	Kick func()

	mu        sync.Mutex
	clean     bool // no repository wants runners and no host holds any
	listeners map[scaleSet]*listening
	images    map[contract.HostName]string // each host's current runner image, once built or found there
	building  map[contract.HostName]bool   // hosts a build-ahead is working on
	retry     map[contract.HostName]time.Time
	serving   map[contract.RepoName]time.Time
	leased    map[contract.RepoName]time.Time
	reaped    map[contract.RepoName]time.Time

	shown   sync.Mutex
	placed  []box.Box
	waiting map[scaleSet][]store.LifecycleBox
}

// scaleSet is one repository's scale set of one size.
type scaleSet struct {
	repo contract.RepoName
	size contract.BoxSizeName
}

type listening struct {
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
	ctx, cancel := context.WithTimeout(ctx, scaleLimit)
	defer cancel()
	view, err := p.survey(ctx)
	if err != nil {
		return err
	}
	failures = append(failures, view.failures...)
	for _, host := range view.hosts {
		p.buildAhead(ctx, host, group)
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
		}
	}
	for key := range wanted {
		if p.listeners[key] != nil || ctx.Err() != nil {
			continue
		}
		listenCtx, cancel := context.WithCancel(ctx)
		l := &listening{cancel: cancel, done: make(chan struct{})}
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
	capacity, err := p.place(ctx, key, assigned, view)
	if err != nil {
		log.Printf("ci pool: repository %s: %s: %v", key.repo, SetName(key.size), err)
	}
	if capacity > 0 {
		if p.serving == nil {
			p.serving = make(map[contract.RepoName]time.Time)
		}
		p.serving[key.repo] = p.Now()
	}
	return capacity
}

// survey is what one pass knows about the fleet. Only ready hosts that answered are in hosts.
type survey struct {
	repos    map[contract.RepoName]store.CIRepo
	hosts    map[contract.HostName]*surveyed
	boxes    []box.Box
	rows     []store.LifecycleBox
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
	for _, repo := range repos {
		s.repos[repo.Repo] = repo
	}
	hosts, err := p.Store.Hosts(ctx)
	if err != nil {
		return s, err
	}
	if s.rows, err = p.Store.LifecycleBoxes(ctx); err != nil {
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
	// A host that did not answer still holds what it held, as far as the node pools know.
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
		ready := slices.DeleteFunc(slices.Clone(open), func(h box.Host) bool { return p.images[h.Name] == "" })
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
		name, err := p.start(ctx, key, view.hosts[host], size)
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
func (p *Pool) start(ctx context.Context, key scaleSet, host *surveyed, size contract.BoxSize) (string, error) {
	image, err := p.prepare(ctx, host)
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
	if err := p.claim(ctx, runnerBox(name, host.host.Name, size)); err != nil {
		return "", err
	}
	id, config, err := p.Sets.JIT(ctx, key.repo, SetName(key.size), name)
	if err != nil {
		return "", jitError{err}
	}
	if err := host.driver.StartRunner(ctx, box.RunnerSpec{Name: name, Image: image, Config: config, Repo: key.repo, ID: id, Slot: slot, Size: size}); err != nil {
		// The next attempt checks the image again, in case a host lost it.
		delete(p.images, host.host.Name)
		// A runner that did start after an uncertain reply loses its registration and exits.
		return "", errors.Join(err, p.GitHub.DeleteRunner(ctx, key.repo, id))
	}
	host.runners = append(host.runners, box.Runner{Name: name, Repo: key.repo, ID: id, Slot: slot, Size: size, Running: true})
	return name, nil
}

// jitError is a failed registration: GitHub's, not the host's.
type jitError struct{ error }

func (e jitError) Unwrap() error { return e.error }

// prepare returns a host's runner image, with its proxy running.
func (p *Pool) prepare(ctx context.Context, host *surveyed) (string, error) {
	image := p.images[host.host.Name]
	if image == "" {
		var err error
		if image, err = host.driver.BuildRunnerImage(ctx); err != nil {
			return "", err
		}
		if p.images == nil {
			p.images = make(map[contract.HostName]string)
		}
		p.images[host.host.Name] = image
	}
	if !host.egress {
		if err := host.driver.EnsureEgress(ctx, image); err != nil {
			return "", err
		}
		host.egress = true
	}
	return image, nil
}

// buildAhead builds a host's runner image in the background when the pool has not seen it there, as after a deploy or on a new
// host. It does not hold p.mu while it builds, so placement goes on, and place prefers hosts whose image is ready. The build
// reuses an image the host already has, so a host that has it is ready within moments.
func (p *Pool) buildAhead(ctx context.Context, host *surveyed, group *sync.WaitGroup) {
	name := host.host.Name
	if !host.host.CI || p.images[name] != "" || p.building[name] {
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
		image, err := host.driver.BuildRunnerImage(buildCtx)
		p.mu.Lock()
		defer p.mu.Unlock()
		delete(p.building, name)
		if err != nil {
			log.Printf("ci pool: host %s: build runner image: %v", name, err)
			return
		}
		if p.images == nil {
			p.images = make(map[contract.HostName]string)
		}
		p.images[name] = image
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
		_, err := p.prepare(ctx, host)
		return err
	}
	return nil
}

// upkeep renews the lease while the pool can serve and removes dead registrations.
func (p *Pool) upkeep(ctx context.Context, repo contract.RepoName, alive map[string]bool) error {
	var failures []error
	now := p.Now()
	if !p.serving[repo].IsZero() && now.Sub(p.serving[repo]) < serveWindow && !now.Before(p.leased[repo]) {
		if err := p.GitHub.RenewLease(ctx, repo, now.Add(leaseFor)); err != nil {
			failures = append(failures, err)
		} else {
			if p.leased == nil {
				p.leased = make(map[contract.RepoName]time.Time)
			}
			p.leased[repo] = now.Add(leaseEvery)
		}
	}
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

// waitsForRoom says whether a session box that waits for CPU or memory would take the host if its runners were gone. Such a
// box comes before a runner: a runner started in each slot that frees up would keep it waiting. A box that fits nowhere, that
// its room's rule keeps off the host, or that other boxes keep out is not waiting on runners and holds none back. The
// snapshot holds no runners, so Admit sees the host as it is without them.
func waitsForRoom(h box.Host, view survey) bool {
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
