package jobs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"dyna-agent/internal/childenv"
	"dyna-agent/internal/machines"
	"dyna-agent/internal/scratch"
	"dyna-agent/internal/shellquote"
)

// DefaultTimeout bounds a job that asked for none.
const DefaultTimeout = 30 * time.Minute

// MachineSource resolves the registered machine a job runs on. machines.Store
// satisfies it.
type MachineSource interface {
	Get(ctx context.Context, name string) (machines.Machine, error)
	GetLocal(ctx context.Context) (machines.Machine, error)
	List(ctx context.Context) ([]machines.Machine, error)
}

// Reporter posts a job's typed events. The engine implements it with the room
// event contract; tests record the calls.
type Reporter interface {
	// JobQueued reports a queued job and why it waits for its node.
	JobQueued(ctx context.Context, job Job) error
	// JobStarted reports a job that began to run.
	JobStarted(ctx context.Context, job Job) error
	// JobFinished reports a job that reached a final state.
	JobFinished(ctx context.Context, job Job) error
}

// NodeBase names the one directory a node keeps on its machine. Local CI and
// jobs share it, and one admission queue bounds both. The user ID is the
// controller's, not the SSH account's.
func NodeBase(node string) string {
	return fmt.Sprintf("/var/tmp/dyna-local-ci-%d/%s", os.Getuid(), node)
}

// RepoKey is the stable folder name of a repository's cache on a node.
func RepoKey(repo string) string {
	sum := sha256.Sum256([]byte(repo))
	return hex.EncodeToString(sum[:16])
}

// Service runs the jobs of one engine. A job is detached on its node: it
// survives the end of the session that submitted it and an engine restart,
// which reattaches through the stored folder and unit.
type Service struct {
	Store    Store
	Machines MachineSource
	// RoomName resolves a room ID for readable placement errors and wait reasons.
	RoomName func(string) string
	Reporter Reporter
	// DataDir is the engine's data folder, where job logs live.
	DataDir string
	// NewExecutor builds the executor of a node. Tests replace it.
	NewExecutor func(Conn) Executor
	// logf logs the service's own errors. Nil uses the standard logger.
	logf func(string, ...any)
	// onRun tells a test that the service began to run a job, synchronously.
	onRun func(Job)
	// onPrepared tells a test that a run won its started write and is about
	// to prepare the node work, synchronously.
	onPrepared func(Job)
	// onAbandoned tells a test that a run gave a job up because the row had
	// ended elsewhere, synchronously.
	onAbandoned func(Job)
	// ClaimEvery is how often a started service re-reads the job table for
	// work another engine queued or cancelled. The default is claimEvery.
	ClaimEvery time.Duration
	// after waits one claim interval; tests replace it with a channel they
	// control. Nil uses time.After.
	after func(time.Duration) <-chan time.Time

	mu      sync.Mutex
	pollers map[string]*Poller
	stops   map[string]func()
	stopped bool
	// wake tells the claim loop that a job was queued. It is nil until the
	// owner starts the service, so a standby engine only queues.
	wake chan struct{}
	// halt ends the claim loop; claimDone closes when it has ended.
	halt      chan struct{}
	claimDone chan struct{}
	wg        sync.WaitGroup
}

func (s *Service) logger() func(string, ...any) {
	if s.logf != nil {
		return s.logf
	}
	return log.Printf
}

func (s *Service) dataDir() string {
	if s.DataDir != "" {
		return s.DataDir
	}
	return defaultDataDir()
}

func (s *Service) executor(conn Conn) Executor {
	if s.NewExecutor != nil {
		return s.NewExecutor(conn)
	}
	return Executor{Node: conn}
}

// RemoteScratchError refuses a command that needs the caller's local
// scratch folder on another node.
type RemoteScratchError struct{}

func (*RemoteScratchError) Error() string {
	return "a remote job cannot use the caller's DYNA_SCRATCH; send files with --workspace or --artifact"
}

// remoteScratchReference recognizes shell references and expanded paths,
// including a path embedded in a shell command argument. Session and
// scratch.Root are trusted engine values, so the path needs no client validation.
func remoteScratchReference(command []string, scratchDir string) bool {
	for _, word := range command {
		if strings.Contains(word, "$DYNA_SCRATCH") || strings.Contains(word, "${DYNA_SCRATCH}") {
			return true
		}
		if scratchDir != "" && containsScratchPath(word, scratchDir) {
			return true
		}
	}
	return false
}

// containsScratchPath finds the scratch root or one of its descendants only
// where shell syntax gives the path a complete start and end.
func containsScratchPath(word, scratchDir string) bool {
	for offset := 0; offset < len(word); {
		at := strings.Index(word[offset:], scratchDir)
		if at < 0 {
			return false
		}
		start := offset + at
		end := start + len(scratchDir)
		if (start == 0 || scratchPathDelimiter(word[start-1])) &&
			(end == len(word) || word[end] == '/' || scratchPathDelimiter(word[end])) {
			return true
		}
		offset = start + 1
	}
	return false
}

func scratchPathDelimiter(b byte) bool {
	switch b {
	case ' ', '\t', '\r', '\n', '\'', '"', ';', '=', '|', '&', '(', ')', '<', '>', ':', ',', '{', '}', '[', ']':
		return true
	default:
		return false
	}
}

// Input is one job to submit.
type Input struct {
	RoomID  string
	Session string
	// Run is the workflow run that submitted the job, when one did.
	Run string
	// IdempotencyKey returns the original job when a workflow retries a call.
	IdempotencyKey string
	// RequestHash verifies that a repeated key names the same request.
	RequestHash string
	// Node names the registered machine. Empty means the local node, unless
	// Labels selects an eligible machine.
	Node    string
	Labels  []string
	Command []string
	// Repo is a local repository path and Ref the commit to check out. Both
	// empty runs the command with no source.
	Repo string
	Ref  string
	Host bool
	// Image runs the command in this container image; Host must be false.
	Image string
	// Network, Mounts and Devices apply only to an image. Mount requests
	// select registrations of the chosen node; they carry no host paths.
	Network NetworkMode
	Mounts  []MountRequest
	Devices []string
	CPUs    int
	Memory  string
	Timeout time.Duration
	// Idle stops a running job after that long without output. Nil disables it.
	Idle *time.Duration
	// Artifacts are paths in the job's work folder to keep after it ends.
	Artifacts []string
	// Heavy marks an IO-heavy job, which a node runs only a few of.
	Heavy bool
	// Resources are validated named units the job holds for its whole run.
	Resources []machines.ResourceName
	// Workspace keeps this session's checkout on the node between jobs. It
	// needs a commit and takes an internal one-unit admission resource.
	Workspace bool
}

// Submit stores a queued job and starts it. The job runs detached, so Submit
// returns as soon as the row is stored.
func (s *Service) Submit(ctx context.Context, in Input) (Job, error) {
	if len(in.IdempotencyKey) > 128 {
		return Job{}, errors.New("a job idempotency key is too long")
	}
	if in.IdempotencyKey != "" {
		existing, err := s.Store.GetByIdempotencyKey(ctx, in.IdempotencyKey)
		if err == nil {
			if existing.RequestHash != in.RequestHash {
				return Job{}, fmt.Errorf("job idempotency key %q was already used for a different request", in.IdempotencyKey)
			}
			return existing, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return Job{}, err
		}
	}
	if len(in.Command) == 0 {
		return Job{}, errors.New("a job needs a command")
	}
	if in.Workspace && (in.Repo == "" || in.Ref == "") {
		return Job{}, errors.New("a kept workspace needs a commit")
	}
	if in.Host && in.Image != "" {
		return Job{}, errors.New("a job runs either on the host or in an image, not both")
	}
	if in.Image == "" && (in.Network != "" || len(in.Mounts) > 0 || len(in.Devices) > 0) {
		return Job{}, errors.New("--network, --mount and --device need --image")
	}
	for _, p := range in.Artifacts {
		if err := checkArtifact(p); err != nil {
			return Job{}, err
		}
	}
	machine, err := s.machine(ctx, in)
	if err != nil {
		return Job{}, err
	}
	if !machine.Enabled {
		return Job{}, fmt.Errorf("node %s is disabled", machine.Name)
	}
	container, err := resolveContainer(machine, in)
	if err != nil {
		return Job{}, err
	}
	// A job runs on the host unless it names an image.
	host := in.Host || in.Image == ""
	// Host execution on another machine runs the submitter's words there, so
	// it needs the machine's host grant, exactly as a host CI check does. The
	// local node is the machine the submitter already works on, so it needs no
	// grant: a one-machine owner runs jobs with no setup.
	if host && machine.SSH != "" && !machine.HostChecksAllowed(in.RoomID) {
		return Job{}, hostGrantError(machine)
	}
	if machine.SSH != "" {
		scratchDir := ""
		if root := scratch.Root(); root != "" && in.Session != "" {
			scratchDir = filepath.Join(root, in.Session)
		}
		if remoteScratchReference(in.Command, scratchDir) {
			return Job{}, &RemoteScratchError{}
		}
	}
	id, err := s.Store.NewID()
	if err != nil {
		return Job{}, err
	}
	job := Job{
		ID: id, RoomID: in.RoomID, Session: in.Session, Run: in.Run, IdempotencyKey: in.IdempotencyKey, RequestHash: in.RequestHash, Node: machine.Name,
		Command: in.Command, Host: host, Image: in.Image, Container: container,
		CPUs: in.CPUs, Timeout: in.Timeout, Heavy: in.Heavy, Artifacts: in.Artifacts,
		Resources: in.Resources, State: StateQueued, Created: s.Store.now(),
		LogPath: filepath.Join(LogDir(s.dataDir(), id), "log"),
	}
	if in.Memory != "" {
		mib, err := ParseMemory(in.Memory)
		if err != nil {
			return Job{}, err
		}
		job.MemoryMiB = mib
	}
	if job.Timeout <= 0 {
		job.Timeout = DefaultTimeout
	}
	if in.Idle != nil {
		if *in.Idle <= 0 {
			return Job{}, errors.New("idle timeout must be positive")
		}
		if *in.Idle > job.Timeout {
			return Job{}, fmt.Errorf("idle timeout %s exceeds job timeout %s", *in.Idle, job.Timeout)
		}
		job.Idle = *in.Idle
	}
	if in.Repo != "" || in.Ref != "" {
		repo, sha, err := s.resolveCommit(ctx, in.Repo, in.Ref)
		if err != nil {
			return Job{}, err
		}
		job.Repo, job.SHA = repo, sha
	}
	if in.Workspace {
		job.Workspace = newWorkspacePath(job.Node, job.Session, job.Repo)
		resource := workspaceResource(job.Session, job.Repo)
		if !slices.Contains(job.Resources, resource) {
			job.Resources = append(job.Resources, resource)
		}
	}
	if err := os.MkdirAll(filepath.Dir(job.LogPath), 0o700); err != nil { // not-state: log blobs live outside SQLite.
		return Job{}, err
	}
	job, err = s.Store.saveSubmitted(ctx, job)
	if err != nil {
		// Another submission with this key may have committed after the first
		// lookup. The unique index picks one winner; return that original job.
		if in.IdempotencyKey != "" {
			if existing, lookupErr := s.Store.GetByIdempotencyKey(ctx, in.IdempotencyKey); lookupErr == nil {
				if existing.RequestHash == in.RequestHash {
					return existing, nil
				}
			}
		}
		return Job{}, err
	}
	// A standby engine, one without the worker lock, only queues the job. The
	// owner claims and starts it, so exactly one executor ever runs a job.
	s.mu.Lock()
	wake := s.wake
	s.mu.Unlock()
	if wake != nil {
		select {
		case wake <- struct{}{}:
		default:
		}
	}
	return job, nil
}

// ParseMemory reads a memory request such as 512m, 16g or 4096 (MiB).
func ParseMemory(request string) (int64, error) {
	request = strings.TrimSpace(strings.ToLower(request))
	if request == "" {
		return 0, errors.New("a memory request is empty")
	}
	unit := request[len(request)-1]
	size := request
	mult := int64(1)
	switch unit {
	case 'g':
		mult = 1024
		size = request[:len(request)-1]
	case 'm':
		size = request[:len(request)-1]
	}
	value, err := strconv.ParseInt(size, 10, 64)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("memory request %q is not a size such as 16g or 512m", request)
	}
	return value * mult, nil
}

// machine resolves the node a job runs on. Labels choose the eligible node
// with the most free CPUs in its queue; no node or labels keeps the local node.
func (s *Service) machine(ctx context.Context, in Input) (machines.Machine, error) {
	if in.Node != "" {
		if len(in.Labels) > 0 {
			return machines.Machine{}, errors.New("--node and --label cannot be used together")
		}
		machine, err := s.Machines.Get(ctx, in.Node)
		if errors.Is(err, os.ErrNotExist) {
			return machine, fmt.Errorf("no node named %s is registered", in.Node)
		}
		return machine, err
	}
	if len(in.Labels) == 0 {
		return s.Machines.GetLocal(ctx)
	}
	list, err := s.Machines.List(ctx)
	if err != nil {
		return machines.Machine{}, err
	}
	for _, label := range in.Labels {
		if !slices.ContainsFunc(list, func(machine machines.Machine) bool { return machine.HasLabel(label) }) {
			return machines.Machine{}, fmt.Errorf("no node has label %q", label)
		}
	}
	chosen := -1
	grantBlocked := -1
	mostFree := -1
	host := in.Host || in.Image == ""
	queue := s.admission()
	for i, machine := range list {
		if !machine.Enabled || in.RoomID != "" && !machine.AllowsRoom(in.RoomID) {
			continue
		}
		matches := true
		for _, label := range in.Labels {
			matches = matches && machine.HasLabel(label)
		}
		if !matches {
			continue
		}
		if host && machine.SSH != "" && !machine.HostChecksAllowed(in.RoomID) {
			grantBlocked = i
			continue
		}
		free := queue.FreeCPUs(ctx, machine.Name, machine.Capabilities.CPUs)
		if free > mostFree {
			chosen, mostFree = i, free
		}
	}
	if chosen < 0 {
		if grantBlocked >= 0 {
			return machines.Machine{}, hostGrantError(list[grantBlocked])
		}
		if in.RoomID != "" {
			room := in.RoomID
			if s.RoomName != nil {
				room = s.RoomName(room)
			}
			return machines.Machine{}, fmt.Errorf("no enabled node serving room %s has every label (%s)", room, strings.Join(in.Labels, ", "))
		}
		return machines.Machine{}, fmt.Errorf("no enabled node has every label (%s)", strings.Join(in.Labels, ", "))
	}
	return list[chosen], nil
}

func hostGrantError(machine machines.Machine) error {
	return fmt.Errorf("node %s allows host jobs only with a host-check grant; the owner or the room root allows them with: dyna node edit %s --allow-host-checks", machine.Name, machine.Name)
}

// resolveCommit reads the exact commit the job checks out. It returns the
// repository's common directory, which keys the node's cache.
func (s *Service) resolveCommit(ctx context.Context, repo, ref string) (string, string, error) {
	if repo == "" {
		return "", "", errors.New("a job with a commit needs the repository path")
	}
	abs, err := filepath.Abs(repo)
	if err != nil {
		return "", "", err
	}
	x := s.executor(Conn{})
	var out strings.Builder
	if err := x.Run(ctx, abs, &out, "git", "rev-parse", "--verify", "--end-of-options", ref+"^{commit}"); err != nil {
		return "", "", fmt.Errorf("resolve commit: %w: %s", err, out.String())
	}
	sha := strings.TrimSpace(out.String())
	if len(sha) < 40 || strings.ContainsAny(sha, " /") {
		return "", "", fmt.Errorf("git returned an invalid commit SHA %q", sha)
	}
	out.Reset()
	if err := x.Run(ctx, abs, &out, "git", "rev-parse", "--path-format=absolute", "--git-common-dir"); err != nil {
		return "", "", fmt.Errorf("resolve repository: %w: %s", err, out.String())
	}
	return strings.TrimSpace(out.String()), sha, nil
}

// Start begins every queued job and reattaches to the running ones an earlier
// engine left on the nodes. The engine calls it once, at start.
func (s *Service) Start(ctx context.Context) {
	s.mu.Lock()
	if s.stopped || s.claimDone != nil {
		s.mu.Unlock()
		return
	}
	wake := make(chan struct{}, 1)
	s.wake = wake
	halt := make(chan struct{})
	s.halt = halt
	done := make(chan struct{})
	s.claimDone = done
	s.mu.Unlock()
	// This engine starts clean: the engine that held queue rows is gone, and
	// the queue rebuilds from the running job rows and the checks this engine
	// reattaches to.
	if err := s.admission().ResetEngine(ctx); err != nil {
		s.logger()("jobs: reset the node queue: %v", err)
	}
	s.claim(ctx)
	running, err := s.Store.Running(ctx)
	if err != nil {
		s.logger()("jobs: read running jobs: %v", err)
	}
	for _, job := range running {
		s.start(job)
	}
	after := s.after
	if after == nil {
		after = time.After
	}
	interval := s.ClaimEvery
	if interval <= 0 {
		interval = claimEvery
	}
	go func() {
		defer close(done)
		for {
			select {
			case <-ctx.Done():
				return
			case <-halt:
				return
			case <-wake:
			case <-after(interval):
			}
			s.mu.Lock()
			stopped := s.stopped
			s.mu.Unlock()
			if stopped {
				return
			}
			workCtx := context.WithoutCancel(ctx)
			s.endQueuedCancels(workCtx)
			s.stopRequested(workCtx)
			s.claim(workCtx)
		}
	}()
}

// claimEvery is how often a started service re-reads the job table. A job
// another engine queues while the owner runs reaches it within one interval.
const claimEvery = 5 * time.Second

// endQueuedCancels ends the queued jobs a cancellation asked for. The
// engine whose sweep took the row posts the job's only event, its cancelled
// end: the session that submitted it never polls.
func (s *Service) endQueuedCancels(ctx context.Context) {
	rows, err := s.Store.EndQueuedCancels(ctx, s.Store.now())
	if err != nil {
		s.logger()("jobs: end cancelled jobs: %v", err)
		return
	}
	for _, row := range rows {
		if s.Reporter != nil {
			if err := s.Reporter.JobFinished(ctx, row); err != nil {
				s.logger()("jobs: report cancelled job %s: %v", row.ID, err)
			}
		}
	}
}

// stopRequested stops the running jobs a cancellation asked for while this
// engine holds the executor: the request stands in the row, so a restart
// that reattached the job recovers it here too.
func (s *Service) stopRequested(ctx context.Context) {
	rows, err := s.Store.RequestedRunning(ctx)
	if err != nil {
		s.logger()("jobs: read cancelled jobs: %v", err)
		return
	}
	s.mu.Lock()
	stops := make(map[string]func(), len(rows))
	for _, row := range rows {
		if stop := s.stops["job:"+row.ID]; stop != nil {
			stops[row.ID] = stop
		}
	}
	s.mu.Unlock()
	for _, stop := range stops {
		stop()
	}
}

// admission is the node queue this engine decides with.
func (s *Service) admission() Admission {
	return Admission{DataDir: s.dataDir(), Machines: s.Machines, RoomName: s.RoomName, Engine: true}
}

// claim admits the queued jobs through the node queue: what fits starts now,
// oldest first with rooms taking turns; the rest waits with a visible reason.
// The pass also decides the waiting checks of the queue table, which share
// every node with jobs.
func (s *Service) claim(ctx context.Context) {
	queued, err := s.Store.ClaimQueued(ctx)
	if err != nil {
		s.logger()("jobs: read queued jobs: %v", err)
		return
	}
	admitted, reasons, err := s.admission().Pass(ctx, queued)
	if err != nil {
		s.logger()("jobs: admission pass: %v", err)
		return
	}
	for _, job := range queued {
		if admitted[job.ID] {
			job.WaitReason = ""
			s.start(job)
			continue
		}
		s.noteWait(ctx, job, reasons[job.ID])
	}
}

// noteWait records why a queued job still waits and posts its queued event,
// so the wait is visible to the session that submitted the job. Each reason
// writes the row, which dyna job show reads; the queued event posts once,
// with the first reason.
func (s *Service) noteWait(ctx context.Context, job Job, reason string) {
	if reason == "" || job.WaitReason == reason {
		return
	}
	job.WaitReason = reason
	if _, _, err := s.Store.SaveOpen(ctx, job); err != nil {
		s.logger()("jobs: record wait of job %s: %v", job.ID, err)
		return
	}
	if s.Reporter != nil {
		if err := s.Reporter.JobQueued(ctx, job); err != nil {
			s.logger()("jobs: report queued job %s: %v", job.ID, err)
		}
	}
}

// Cancel stops a queued or running job. A queued job never starts.
func (s *Service) Cancel(ctx context.Context, id string) (Job, error) {
	// One durable request, then wake this engine's loop. Cancel never stops
	// a process, never ends a row and never posts an event: the engine that
	// owns the job carries the request to the recorded end - a queued row on
	// the next tick of any engine, a running one on the next tick of the
	// engine that holds its executor.
	if err := s.Store.RequestCancel(ctx, id); err != nil {
		return Job{}, err
	}
	s.mu.Lock()
	wake := s.wake
	s.mu.Unlock()
	if wake != nil {
		select {
		case wake <- struct{}{}:
		default:
		}
	}
	return s.Store.Get(ctx, id)
}

// Stop ends the service: it stops polling and cancels the engine-side waits.
// The jobs keep running on their nodes, and the restarted engine reattaches.
func (s *Service) Stop() {
	s.mu.Lock()
	s.stopped = true
	stops := s.stops
	s.stops = nil
	halt := s.halt
	s.halt = nil
	done := s.claimDone
	s.mu.Unlock()
	for _, stop := range stops {
		if stop != nil {
			stop()
		}
	}
	if halt != nil {
		close(halt)
	}
	if done != nil {
		<-done
	}
	s.wg.Wait()
}

// poller returns the one poller of a node, so every job on it shares one poll
// call. It returns nil when the service has stopped.
func (s *Service) poller(conn Conn) *Poller {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return nil
	}
	if s.pollers == nil {
		s.pollers = map[string]*Poller{}
	}
	if s.stops == nil {
		s.stops = map[string]func(){}
	}
	if p := s.pollers[conn.Name]; p != nil {
		return p
	}
	x := s.executor(conn)
	p, stop := x.NewPoller(context.Background())
	// The poller is one per node and shared by every job on it, so its
	// callbacks are set here, once, under the service lock - never per run,
	// where one job would overwrite another's callback and race the loop.
	// The launch record names the job the node accepted, from the spec.
	p.Launched = func(spec RunSpec) { s.Store.MarkLaunched(context.Background(), spec.ID) }
	s.pollers[conn.Name] = p
	s.stops["poller:"+conn.Name] = stop
	return p
}

// start runs one job on its node. It returns at once; the work happens in its
// own goroutine. The job's cancel is registered before the goroutine starts,
// so a cancel that races the start still reaches it. A job this process
// already runs is not started twice.
func (s *Service) start(job Job) {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	if s.stops == nil {
		s.stops = map[string]func(){}
	}
	if _, running := s.stops["job:"+job.ID]; running {
		s.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.stops["job:"+job.ID] = cancel
	// The wait group grows under the mutex that Stop also takes, so Stop can
	// never pass its Wait between this registration and the goroutine.
	s.wg.Add(1)
	s.mu.Unlock()
	go s.run(job, ctx, cancel)
}

// run executes one job from submission or reattach to its recorded end.
func (s *Service) run(job Job, ctx context.Context, cancel context.CancelFunc) {
	defer s.wg.Done()
	defer cancel()
	if s.onRun != nil {
		s.onRun(job)
	}
	defer func() {
		s.mu.Lock()
		delete(s.stops, "job:"+job.ID)
		s.mu.Unlock()
	}()

	machine, err := s.Machines.Get(context.Background(), job.Node)
	if err != nil {
		s.finish(&job, err)
		return
	}
	conn := Conn{Name: machine.Name, SSH: machine.SSH, SSHConnectTimeout: machine.SSHConnectTimeout}
	reattach := job.State == StateRunning
	// A running row relaunches when the crashed engine never completed the
	// launch: either the start event is missing - the crash fell before it -
	// or the node never accepted the unit - the crash, or a node-side death,
	// fell inside the start, after the folder was made but before the unit
	// ran. A row whose start went out and whose unit was accepted attaches:
	// relaunching that would run the job twice.
	launch := !reattach || !job.StartPosted || !job.Launched
	if reattach && launch && !job.StartPosted {
		// The row's start event was lost to the crash; post it now, before
		// the fresh launch, and let the pre-poll save record it.
		if s.Reporter != nil {
			if err := s.Reporter.JobStarted(ctx, job); err != nil {
				s.logger()("jobs: report started job %s: %v", job.ID, err)
			}
		}
		job.StartPosted = true
	}
	if !reattach {
		job.State = StateRunning
		job.Started = s.Store.now()
		// The transition to running is conditional: a cancel that reached the
		// row between the claim and this write wins, the node never sees the
		// job, and the canceller's own report stands.
		if _, won, err := s.Store.SaveOpen(ctx, job); err != nil {
			s.logger()("jobs: record started job %s: %v", job.ID, err)
		} else if !won {
			// The canceller ended the row before this run recorded its start; it
			// reports the end, so this run stays quiet.
			if s.onAbandoned != nil {
				s.onAbandoned(job)
			}
			return
		}
		// The start goes out with the durable transition, before any node work
		// can fail: whatever ends the job later, its end follows its start.
		if s.Reporter != nil {
			if err := s.Reporter.JobStarted(ctx, job); err != nil {
				s.logger()("jobs: report started job %s: %v", job.ID, err)
			}
		}
		job.StartPosted = true
		if s.onPrepared != nil {
			s.onPrepared(job)
		}
	}
	job.Dir = path.Join(NodeBase(machine.Name), "jobs", job.ID)
	job.Unit = "dyna-job-" + job.ID + ".service"
	resources := ClampResources(job.Host, job.CPUs, job.MemoryMiB, machineFacts(machine))
	job.Note = resources.Note

	if err := os.MkdirAll(filepath.Dir(job.LogPath), 0o700); err != nil { // not-state: log blobs live outside SQLite.
		s.finish(&job, err)
		return
	}
	flag := os.O_WRONLY | os.O_CREATE
	if !launch {
		flag |= os.O_APPEND
	} else {
		flag |= os.O_TRUNC
	}
	logFile, err := os.OpenFile(job.LogPath, flag, 0o600)
	if err != nil {
		s.finish(&job, err)
		return
	}
	defer logFile.Close()
	logWriter := &logStore{job: &job, file: logFile}
	// The log file is the durable record of what the engine wrote. A crash can
	// lose the row offset between a poll write and its save, so the file size
	// recovers it and the poll continues exactly where the file ends.
	if fi, err := os.Stat(job.LogPath); err == nil && fi.Size() != job.LogBytes {
		job.LogBytes = fi.Size()
	}

	x := s.executor(conn)
	if launch && job.SHA != "" {
		cache := path.Join(NodeBase(machine.Name), RepoKey(job.Repo)+".git")
		if err := x.PushCommit(ctx, job.Repo, cache, job.SHA); err != nil {
			s.finish(&job, err)
			return
		}
	}
	spec := RunSpec{ID: job.ID, Dir: path.Join(job.Dir, "detached"), Unit: job.Unit, Attach: !launch, Offset: job.LogBytes, Idle: job.Idle}
	if job.Image != "" {
		spec.Container = containerName(job.ID)
	}
	if !job.Host {
		limits := resources.Scope(false)
		spec.Limits = &limits
	}
	if launch {
		// A detached unit cannot inherit the CLI environment. Rebuild the
		// caller's folder from the session identity stored at submission.
		scratchDir := ""
		if machine.SSH == "" && job.Session != "" {
			scratchDir, err = scratch.Ensure(job.Session)
			if err != nil {
				s.finish(&job, err)
				return
			}
		}
		spec.Script = jobScript(machine, job, resources, scratchDir)
		// A recovered launch of a unit the node already accepted - the engine
		// died between the unit starting and the launch record - starts
		// nothing: the node's start is locked and idempotent, and an active
		// unit or an exit record makes it return at once. The run follows the
		// unit that is already there; stopping it would kill work that ran.
		if _, won, err := s.Store.SaveOpen(ctx, job); err != nil {
			s.logger()("jobs: record started job %s: %v", job.ID, err)
		} else if !won {
			// The row ended elsewhere while the run prepared. This run posted
			// the start, so it reports the end through finish, which adopts
			// the row's own terminal state.
			if s.onAbandoned != nil {
				s.onAbandoned(job)
			}
			s.finish(&job, nil)
			return
		} else if fresh, err := s.Store.Get(ctx, job.ID); err == nil && fresh.CancelRequested {
			// A cancellation asked for the job while the run prepared. The
			// node never sees it; the end follows the start already posted.
			s.finish(&job, nil)
			return
		}
	}

	poller := s.poller(conn)
	if poller == nil {
		return
	}
	// The node enforces the deadline itself; the engine allows a slow route
	// on top before it stops following the job. The queue wait is over: the
	// job was admitted before it started.
	var timeoutCancel context.CancelFunc
	runCtx, timeoutCancel := context.WithTimeout(ctx, job.Timeout+2*time.Minute)
	defer timeoutCancel()
	runErr := poller.Run(runCtx, spec, logWriter)
	if runErr != nil && errors.Is(runErr, context.Canceled) && EngineStopping() {
		// The engine is stopping: the job keeps running on its node and the
		// restarted engine reattaches through this row. The offset write is
		// conditional too, so it cannot revive a row another engine ended.
		_, _, _ = s.Store.SaveOpen(context.WithoutCancel(ctx), logWriter.snapshot())
		return
	}
	var idle *idleTimeoutError
	if runErr != nil && !job.State.Finished() && !errors.As(runErr, &idle) {
		s.stopUnit(x, spec)
	}
	// An idle run already stopped its unit and drained its log in Poller.Run.
	s.finish(&job, runErr)
}

// stopUnit asks the node to stop the job's unit, best effort.
func (s *Service) stopUnit(x Executor, spec RunSpec) {
	stopCtx, cancel := context.WithTimeout(context.Background(), StopTimeout)
	defer cancel()
	_ = x.StopDetached(stopCtx, spec)
}

// classify names the state a job ended in from its run error.
func classify(err error) (State, *int) {
	switch {
	case err == nil:
		return StatePassed, ptr(0)
	case errors.Is(err, context.DeadlineExceeded):
		return StateTimedOut, nil
	case errors.Is(err, context.Canceled):
		return StateCancelled, nil
	}
	var exit *DetachedExit
	if errors.As(err, &exit) {
		switch {
		case exit.Code == 0:
			return StatePassed, ptr(0)
		case exit.Code == 124:
			return StateTimedOut, ptr(124)
		}
		return StateFailed, ptr(exit.Code)
	}
	var lost *NodeLostError
	if errors.As(err, &lost) {
		return StateLost, nil
	}
	var idle *idleTimeoutError
	if errors.As(err, &idle) {
		return StateTimedOut, nil
	}
	return StateFailed, nil
}

func ptr(code int) *int { return &code }

// finish records a job end and reports it, through the live job so the
// stored row keeps the log offset the run reached. The job folder on the
// node, its checkout included, is removed at the end through the machine
// connection: a fresh job gets a fresh folder.
func (s *Service) finish(job *Job, err error) {
	state, exit := StateFailed, (*int)(nil)
	if err == nil {
		state, exit = StatePassed, ptr(0)
	} else {
		state, exit = classify(err)
		job.Note = err.Error()
	}
	// The launch mark is written to the row, not to this copy, so the row
	// says whether the node ever ran the job.
	launched := job.Launched
	if row, readErr := s.Store.Get(context.Background(), job.ID); readErr == nil {
		launched = launched || row.Launched
		if row.CancelRequested {
			// A cancellation asked for the job; whatever ended the run, the
			// recorded end is the cancelled one.
			state, exit, job.Note = StateCancelled, nil, "cancelled"
		}
	}
	job.State = state
	job.ExitCode = exit
	job.Ended = s.Store.now()
	ctx := context.Background()
	// The artifacts come back before the end is recorded and posted, so the
	// woken submitter can fetch them, and before the folder is removed. A copy
	// that fails is noted on the job; it does not change the job's result.
	if len(job.Artifacts) > 0 && launched && job.Dir != "" && !EngineStopping() {
		note, err := "", error(nil)
		if machine, e := s.Machines.Get(ctx, job.Node); e != nil {
			err = e
		} else {
			conn := Conn{Name: machine.Name, SSH: machine.SSH, SSHConnectTimeout: machine.SSHConnectTimeout}
			note, err = copyArtifacts(ctx, s.executor(conn), *job, ArtifactDir(job.LogPath))
		}
		if err != nil {
			note = "artifacts: " + err.Error()
		}
		if note != "" {
			job.Note = strings.TrimPrefix(job.Note+"; "+note, "; ")
		}
	}
	// The end is a conditional write: another engine may have cancelled the
	// row while this one held the executor. The row's own terminal state and
	// report then stay, and this engine stops the node process quietly.
	row, won, err := s.Store.SaveOpen(ctx, *job)
	if err != nil {
		s.logger()("jobs: record finished job %s: %v", job.ID, err)
	}
	if !won {
		// The row ended elsewhere. One engine ends a job, so this can only
		// be a row this run already ended, or one another engine swept while
		// this one still held a stale copy: the winner posted, so adopt its
		// end quietly.
		job.State, job.ExitCode, job.Ended = row.State, row.ExitCode, row.Ended
		return
	}
	if s.Reporter != nil {
		if err := s.Reporter.JobFinished(ctx, *job); err != nil {
			s.logger()("jobs: report finished job %s: %v", job.ID, err)
		}
	}
	if job.Dir != "" && !EngineStopping() {
		machine, err := s.Machines.Get(ctx, job.Node)
		if err != nil {
			s.logger()("jobs: remove the folder of job %s: resolve node %s: %v", job.ID, job.Node, err)
			return
		}
		conn := Conn{Name: machine.Name, SSH: machine.SSH, SSHConnectTimeout: machine.SSHConnectTimeout}
		x := s.executor(conn)
		removeCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		var out TailBuffer
		if e := x.NodeCommand(removeCtx, &out, &out, "rm -rf -- "+shellquote.Quote(job.Dir)); e != nil {
			s.logger()("jobs: remove the folder of job %s on %s: %v: %s", job.ID, job.Node, e, out.String())
		}
	}
}

// machineFacts reads a machine's clamp facts: its last probe. A machine with
// no probe keeps the job's own request.
func machineFacts(m machines.Machine) Facts {
	return Facts{CPUs: m.Capabilities.CPUs, MemoryBytes: m.Capabilities.MemTotal}
}

// logStore streams a job log to its file and tracks the stored size on the
// live job, so a save keeps the offset the run reached. The lock makes the
// file bytes and the tracked offset one unit: a save through snapshot never
// records an offset that a poll write has not reached the file with yet.
type logStore struct {
	mu   sync.Mutex
	job  *Job
	file *os.File
}

func (l *logStore) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	n, err := l.file.Write(p)
	l.job.LogBytes += int64(n)
	return n, err
}

// snapshot copies the live job once no poll write is in flight.
func (l *logStore) snapshot() Job {
	l.mu.Lock()
	defer l.mu.Unlock()
	return *l.job
}

// workDir is the command's working directory and the root of artifact paths.
func workDir(job Job) string {
	if job.Workspace != "" {
		return job.Workspace
	}
	return path.Join(job.Dir, "work")
}

// workspaceCheckoutScript resets a kept checkout to one exact commit. Its git
// worktree stays in place, and clean without -x preserves ignored build output.
func workspaceCheckoutScript(cache, work, sha string) string {
	q := shellquote.Quote
	lock := cache + ".worktrees.lock"
	return "if [ ! -e " + q(path.Join(work, ".git")) + " ]; then flock " + q(lock) + " git --git-dir=" + q(cache) + " worktree add --detach " + q(work) + " " + q(sha) +
		"; else git -C " + q(work) + " checkout --detach --force " + q(sha) + "; fi; git -C " + q(work) + " clean -fd"
}

// containerName is the Docker container name an image job runs under.
func containerName(id string) string {
	return "dyna-job-" + id
}

// jobScript is the script a job runs on its node: it checks out the commit
// when the job has one and runs the command under the job's deadline. The
// node queue admitted the job before it started, so the script waits for
// nothing.
func jobScript(machine machines.Machine, job Job, resources Resources, scratchDir string) string {
	q := shellquote.Quote
	base := NodeBase(machine.Name)
	work := workDir(job)
	seconds := int64((job.Timeout + time.Second - 1) / time.Second)
	inner := "set -euo pipefail; "
	if scratchDir != "" {
		inner += "export " + scratch.Env + "=" + q(scratchDir) + "; "
	} else {
		inner += "unset " + scratch.Env + "; "
	}
	inner += "export " + strings.Join(childenv.NoEditor(), " ") + "; "
	inner += "mkdir -p " + q(path.Dir(work)) + "; "
	if job.SHA != "" {
		cache := path.Join(base, RepoKey(job.Repo)+".git")
		if job.Workspace != "" {
			inner += workspaceCheckoutScript(cache, work, job.SHA) + "; "
		} else {
			inner += "flock " + q(cache+".worktrees.lock") + " git --git-dir=" + q(cache) + " worktree add --detach " + q(work) + " " + q(job.SHA) + " && "
		}
	} else {
		// A job with no source still gets its own work folder, so its outputs
		// have one place to live and --artifact paths one root.
		inner += "mkdir -p " + q(work) + "; "
	}
	// Each word is quoted on its own, so the shell that runs the script builds
	// exactly the argv the submitter passed: no boundary moves, no
	// metacharacter of an argument runs.
	quoted := make([]string, 0, len(job.Command))
	for _, word := range job.Command {
		quoted = append(quoted, q(word))
	}
	command := strings.Join(quoted, " ")
	if job.Image != "" {
		// The container runs the argv itself, so an image needs no shell:
		// alpine or distroless images have no bash. The first word replaces the
		// image's entrypoint, which would otherwise take the command as its
		// own arguments.
		// A user manager that started before its user joined the docker group
		// keeps the old groups, on this node or over SSH alike.
		inner += "docker=(docker); if ! docker info >/dev/null 2>&1; then docker=(sudo -n docker); fi; "
		// The work folder lives on the node, so the container mounts it and
		// works there, exactly as a CI check container does, and what the job
		// writes there stays for its artifacts.
		mount := " -v " + q(work+":/workspace") + " -w /workspace"
		for _, registered := range job.Container.Mounts {
			mount += " -v " + q(registered.Source+":"+registered.Target+":"+string(registered.Mode))
		}
		for _, device := range job.Container.Devices {
			mount += " --device=" + q(device)
		}
		network := ""
		if job.Container.Network == NetworkNone {
			network = " --network=none"
		}
		// The container has a known name, so the wrapper and the node stop can
		// stop it. timeout kills only the docker client, which leaves the
		// container running. The trap stops it when this script exits.
		name := containerName(job.ID)
		inner += ContainerStopFunction + "trap " + q("dyna_stop_container "+name) + " EXIT; trap 'exit 143' TERM; "
		inner += "timeout --signal=TERM --kill-after=10s " + strconv.FormatInt(seconds, 10) + "s \"${docker[@]}\" run --rm --init --name " + q(name) + " --cpus=" + strconv.Itoa(resources.CPUs) +
			" --memory=" + DockerMemory(resources.MemoryMiB) + " --memory-swap=" + DockerMemory(resources.MemoryMiB) +
			" --pids-limit=512" + network + mount + " --entrypoint " + quoted[0] + " " + q(job.Image) + " " + strings.Join(quoted[1:], " ")
	} else {
		inner += "cd " + q(work) + "; "
		inner += "timeout --foreground --signal=TERM --kill-after=10s " + strconv.FormatInt(seconds, 10) + "s bash -o pipefail -c " + q(command)
	}
	return inner
}
