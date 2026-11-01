// Package ci serves a repository's GitHub Actions jobs from one runner scale
// set. A listener long-polls GitHub, and each job it reports gets one ephemeral
// runner on a host that admission picks. See next/docs/ci-runners.md.
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
)

const (
	// Name is the scale set's name, and the one label a workflow's runs-on names.
	Name       = "dyna"
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

// Scale receives the number of jobs assigned to a repository's scale set and
// returns how many runners the pool can hold right now.
type Scale func(ctx context.Context, assigned int) (capacity int)

// Scaleset hides GitHub's scale set client, which is a preview API.
type Scaleset interface {
	// Listen joins repo's scale set, creating it when needed, and calls scale on
	// every poll until ctx ends or the session fails.
	Listen(ctx context.Context, repo contract.RepoName, scale Scale) error
	// JIT returns a runner ID and its single-job config, which is a secret.
	JIT(ctx context.Context, repo contract.RepoName, name string) (int64, string, error)
	// Delete removes repo's scale set.
	Delete(ctx context.Context, repo contract.RepoName) error
}

// Store reads the settings and the placement inputs.
type Store interface {
	CIHosts(context.Context) ([]contract.RepoCIHost, error)
	Hosts(context.Context) ([]box.Host, error)
	Boxes(context.Context) ([]box.Box, error)
}

// Pool owns the listeners. Run and every Scale share mu, so one placement at a
// time sees the runners of the one before it. Fields after Now are guarded by mu.
type Pool struct {
	Store  Store
	Hosts  func(context.Context, contract.HostName) (Driver, error)
	GitHub API
	Sets   Scaleset
	// Memory reads a host's latest memory use, as the box reconciler does.
	Memory func(contract.HostName) int64
	Now    func() time.Time

	mu        sync.Mutex
	clean     bool // no repository wants runners and no host holds any
	listeners map[contract.RepoName]*listening
	images    map[contract.HostName]string
	retry     map[contract.HostName]time.Time
	serving   map[contract.RepoName]time.Time
	leased    map[contract.RepoName]time.Time
	reaped    map[contract.RepoName]time.Time
}

type listening struct {
	cancel  context.CancelFunc
	done    chan struct{}
	err     error
	leaving bool
}

// Run keeps the listeners until ctx ends. Runners outlive the engine: a job in
// progress finishes, and the next listener counts its container.
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

// survey is what one pass knows about the fleet. Only hosts that answered are in it.
type survey struct {
	settings []contract.RepoCIHost
	hosts    map[contract.HostName]*surveyed
	boxes    []box.Box
	failures []error
}

type surveyed struct {
	host    box.Host
	driver  Driver
	runners []box.Runner
	egress  bool
}

func (p *Pool) survey(ctx context.Context) (survey, error) {
	var s survey
	var err error
	if s.settings, err = p.Store.CIHosts(ctx); err != nil {
		return s, err
	}
	hosts, err := p.Store.Hosts(ctx)
	if err != nil {
		return s, err
	}
	if s.boxes, err = p.Store.Boxes(ctx); err != nil {
		return s, err
	}
	s.hosts = make(map[contract.HostName]*surveyed)
	for _, host := range hosts {
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
			continue
		}
		s.hosts[host.Name] = &surveyed{host: host, driver: driver, runners: runners, egress: egress}
	}
	return s, nil
}

// Tick runs the listeners the settings ask for and clears what they no longer do.
func (p *Pool) Tick(ctx context.Context, group *sync.WaitGroup) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	settings, err := p.Store.CIHosts(ctx)
	if err != nil {
		return err
	}
	wanted := make(map[contract.RepoName]bool)
	for _, setting := range settings {
		wanted[setting.Repo] = true
	}
	failures := p.steer(ctx, wanted, group)
	if len(settings) == 0 && p.clean {
		return errors.Join(failures...)
	}
	view, err := p.survey(ctx)
	if err != nil {
		return err
	}
	failures = append(failures, view.failures...)
	alive := make(map[contract.RepoName]map[string]bool)
	for _, host := range view.hosts {
		if err := p.sweep(ctx, host, view.settings, alive); err != nil {
			failures = append(failures, fmt.Errorf("host %s: %w", host.host.Name, err))
		}
	}
	for repo := range wanted {
		if err := p.upkeep(ctx, repo, alive[repo]); err != nil {
			failures = append(failures, fmt.Errorf("repository %s: %w", repo, err))
		}
	}
	// One pass after the last repository leaves removes its runners. A pass that
	// failed looks again, and the first pass reaps what an earlier engine left.
	p.clean = len(settings) == 0 && len(failures) == 0
	return errors.Join(failures...)
}

// steer starts missing listeners, restarts failed ones on the next tick, and
// deletes the scale set of a repository that left.
func (p *Pool) steer(ctx context.Context, wanted map[contract.RepoName]bool, group *sync.WaitGroup) []error {
	var failures []error
	if p.listeners == nil {
		p.listeners = make(map[contract.RepoName]*listening)
	}
	for repo, l := range p.listeners {
		finished := false
		select {
		case <-l.done:
			finished = true
		default:
		}
		switch {
		case !finished && !wanted[repo]:
			l.cancel()
			l.leaving = true
		case finished && l.leaving:
			if err := p.Sets.Delete(ctx, repo); err != nil {
				failures = append(failures, fmt.Errorf("repository %s: delete scale set: %w", repo, err))
				continue
			}
			delete(p.listeners, repo)
		case finished:
			if ctx.Err() == nil {
				failures = append(failures, fmt.Errorf("repository %s: listener stopped: %w", repo, l.err))
			}
			delete(p.listeners, repo)
		}
	}
	for repo := range wanted {
		if p.listeners[repo] != nil || ctx.Err() != nil {
			continue
		}
		listenCtx, cancel := context.WithCancel(ctx)
		l := &listening{cancel: cancel, done: make(chan struct{})}
		p.listeners[repo] = l
		group.Add(1)
		go func() {
			defer group.Done()
			defer close(l.done)
			defer cancel()
			l.err = p.Sets.Listen(listenCtx, repo, func(scaleCtx context.Context, assigned int) int {
				return p.scale(scaleCtx, repo, assigned)
			})
		}()
	}
	return failures
}

// sweep removes runners nothing wants, and the proxy once a host needs none.
func (p *Pool) sweep(ctx context.Context, host *surveyed, settings []contract.RepoCIHost, alive map[contract.RepoName]map[string]bool) error {
	wanted := make(map[contract.RepoName]bool)
	for _, setting := range settings {
		if setting.Host == host.host.Name {
			wanted[setting.Repo] = true
		}
	}
	for _, runner := range host.runners {
		if !wanted[runner.Repo] {
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
	}
	if len(wanted) == 0 && host.egress {
		return host.driver.RemoveEgress(ctx)
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

// reap removes registrations no container answers for: a runner killed before
// it finished a job, or one on a host that dropped off the network. A runner
// whose registration goes exits by itself when its host returns.
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

// scale runs on every long poll, so it also retries whatever the last pass could
// not do. It never fails: a host problem must not end the listener.
func (p *Pool) scale(ctx context.Context, repo contract.RepoName, assigned int) int {
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
	capacity, err := p.place(ctx, repo, assigned, view)
	if err != nil {
		log.Printf("ci pool: repository %s: %v", repo, err)
	}
	if capacity > 0 {
		if p.serving == nil {
			p.serving = make(map[contract.RepoName]time.Time)
		}
		p.serving[repo] = p.Now()
	}
	return capacity
}

// runnerBox makes a runner visible to admission. It has an image, so admission
// counts its CPUs and memory but no disk: all it writes goes to tmpfs.
func runnerBox(name string, host contract.HostName, size contract.BoxSize) box.Box {
	return box.Box{ID: contract.BoxID(name), Host: host, Image: "runner", Size: size, State: contract.BoxReady}
}

// place starts runners until the assigned jobs have one, and returns the capacity.
func (p *Pool) place(ctx context.Context, repo contract.RepoName, assigned int, view survey) (int, error) {
	var rows []contract.RepoCIHost
	for _, setting := range view.settings {
		if setting.Repo == repo {
			rows = append(rows, setting)
		}
	}
	if len(rows) == 0 {
		return 0, nil
	}
	size, ok := box.Size(rows[0].Size)
	if !ok {
		return 0, fmt.Errorf("invalid runner size %q", rows[0].Size)
	}
	boxes := slices.Clone(view.boxes)
	for _, host := range view.hosts {
		for _, runner := range host.runners {
			if runner.Running {
				boxes = append(boxes, runnerBox(runner.Name, host.host.Name, size))
			}
		}
	}
	mine := make(map[contract.HostName]int)
	running := 0
	for _, host := range view.hosts {
		for _, runner := range host.runners {
			if runner.Running && runner.Repo == repo {
				mine[host.host.Name]++
				running++
			}
		}
	}
	request := box.Box{ID: "dyna-ci-request", Room: rows[0].Room, Size: size}
	// open lists hosts with room for one more, and the rows that name them.
	open := func() []box.Host {
		var hosts []box.Host
		for _, row := range rows {
			host := view.hosts[row.Host]
			if host != nil && mine[row.Host] < row.Runners && !p.Now().Before(p.retry[row.Host]) {
				hosts = append(hosts, host.host)
			}
		}
		return hosts
	}
	capacity := 0
	for _, row := range rows {
		// A host below its disk floor or out of memory reports only what it holds,
		// so GitHub stops assigning it more.
		capacity += mine[row.Host]
		if host := view.hosts[row.Host]; host != nil && box.Admit([]box.Host{host.host}, boxes, request).Host != "" {
			capacity = capacity - mine[row.Host] + max(row.Runners, mine[row.Host])
		}
	}
	var failures []error
	for running < min(assigned, capacity) {
		placement := box.Admit(open(), boxes, request)
		if placement.Host == "" {
			break
		}
		name, err := p.start(ctx, repo, view.hosts[placement.Host], size)
		if err != nil {
			failures = append(failures, fmt.Errorf("host %s: %w", placement.Host, err))
			if p.retry == nil {
				p.retry = make(map[contract.HostName]time.Time)
			}
			p.retry[placement.Host] = p.Now().Add(retryAfter)
			continue
		}
		boxes = append(boxes, runnerBox(name, placement.Host, size))
		mine[placement.Host]++
		running++
	}
	return capacity, errors.Join(failures...)
}

// start prepares a host and starts one runner on it.
func (p *Pool) start(ctx context.Context, repo contract.RepoName, host *surveyed, size contract.BoxSize) (string, error) {
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
	var raw [4]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	name := namePrefix + string(host.host.Name) + "-" + hex.EncodeToString(raw[:])
	id, config, err := p.Sets.JIT(ctx, repo, name)
	if err != nil {
		return "", err
	}
	if err := host.driver.StartRunner(ctx, box.RunnerSpec{Name: name, Image: image, Config: config, Repo: repo, ID: id, Size: size}); err != nil {
		// The next attempt checks the image again, in case a host lost it.
		delete(p.images, host.host.Name)
		// A runner that did start after an uncertain reply loses its registration and exits.
		return "", errors.Join(err, p.GitHub.DeleteRunner(ctx, repo, id))
	}
	return name, nil
}
