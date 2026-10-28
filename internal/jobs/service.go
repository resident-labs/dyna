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
	"strconv"
	"strings"
	"sync"
	"time"

	"dyna-agent/internal/machines"
	"dyna-agent/internal/shellquote"
)

// DefaultTimeout bounds a job that asked for none.
const DefaultTimeout = 30 * time.Minute

// MachineSource resolves the registered machine a job runs on. machines.Store
// satisfies it.
type MachineSource interface {
	Get(ctx context.Context, name string) (machines.Machine, error)
	GetLocal(ctx context.Context) (machines.Machine, error)
}

// Reporter posts a job's typed events. The engine implements it with the room
// event contract; tests record the calls.
type Reporter interface {
	// JobStarted reports a job that began to run.
	JobStarted(ctx context.Context, job Job) error
	// JobFinished reports a job that reached a final state.
	JobFinished(ctx context.Context, job Job) error
}

// NodeBase names the one directory a node keeps on its machine. Local CI and
// jobs share it, so a node's CI slots bound both. The user ID is the
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
	Reporter Reporter
	// DataDir is the engine's data folder, where job logs live.
	DataDir string
	// NewExecutor builds the executor of a node. Tests replace it.
	NewExecutor func(Conn) Executor
	// logf logs the service's own errors. Nil uses the standard logger.
	logf func(string, ...any)
	// onRun tells a test that the service began to run a job, synchronously.
	onRun func(Job)
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

// Input is one job to submit.
type Input struct {
	RoomID  string
	Session string
	// Node names the registered machine. Empty means the local node.
	Node    string
	Command []string
	// Repo is a local repository path and Ref the commit to check out. Both
	// empty runs the command with no source.
	Repo string
	Ref  string
	Host bool
	// Image runs the command in this container image; Host must be false.
	Image   string
	CPUs    int
	Memory  string
	Timeout time.Duration
}

// Submit stores a queued job and starts it. The job runs detached, so Submit
// returns as soon as the row is stored.
func (s *Service) Submit(ctx context.Context, in Input) (Job, error) {
	if len(in.Command) == 0 {
		return Job{}, errors.New("a job needs a command")
	}
	if in.Host && in.Image != "" {
		return Job{}, errors.New("a job runs either on the host or in an image, not both")
	}
	machine, err := s.machine(ctx, in.Node)
	if err != nil {
		return Job{}, err
	}
	if !machine.Enabled {
		return Job{}, fmt.Errorf("machine %s is disabled", machine.Name)
	}
	// A job runs on the host unless it names an image.
	host := in.Host || in.Image == ""
	// Host execution on another machine runs the submitter's words there, so
	// it needs the machine's host grant, exactly as a host CI check does. The
	// local node is the machine the submitter already works on, so it needs no
	// grant: a one-machine owner runs jobs with no setup.
	if host && machine.SSH != "" && !machine.HostChecksAllowed(in.RoomID) {
		return Job{}, fmt.Errorf("machine %s allows host jobs only with a host-check grant; the owner or the room root allows them with: dyna machine edit %s --allow-host-checks", machine.Name, machine.Name)
	}
	id, err := s.Store.NewID()
	if err != nil {
		return Job{}, err
	}
	job := Job{
		ID: id, RoomID: in.RoomID, Session: in.Session, Node: machine.Name,
		Command: in.Command, Host: host, Image: in.Image,
		CPUs: in.CPUs, Timeout: in.Timeout,
		State: StateQueued, Created: s.Store.now(),
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
	if in.Repo != "" || in.Ref != "" {
		repo, sha, err := s.resolveCommit(ctx, in.Repo, in.Ref)
		if err != nil {
			return Job{}, err
		}
		job.Repo, job.SHA = repo, sha
	}
	if err := os.MkdirAll(filepath.Dir(job.LogPath), 0o700); err != nil { // not-state: log blobs live outside SQLite.
		return Job{}, err
	}
	if err := s.Store.Save(ctx, job); err != nil {
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

// machine resolves the node a job runs on. Empty means the local node.
func (s *Service) machine(ctx context.Context, name string) (machines.Machine, error) {
	if name == "" {
		return s.Machines.GetLocal(ctx)
	}
	machine, err := s.Machines.Get(ctx, name)
	if errors.Is(err, os.ErrNotExist) {
		return machine, fmt.Errorf("no machine named %s is registered", name)
	}
	return machine, err
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
			s.claim(workCtx)
			s.stopCancelled(workCtx)
		}
	}()
}

// claimEvery is how often a started service re-reads the job table. A job
// another engine queues while the owner runs reaches it within one interval.
const claimEvery = 5 * time.Second

// stopCancelled stops the running jobs another engine cancelled: their rows
// say cancelled while this engine still holds the executor, so the node
// process must stop and the row keeps its cancelled state.
func (s *Service) stopCancelled(ctx context.Context) {
	s.mu.Lock()
	stops := make(map[string]func(), len(s.stops))
	for key, stop := range s.stops {
		if id, ok := strings.CutPrefix(key, "job:"); ok {
			stops[id] = stop
		}
	}
	s.mu.Unlock()
	for id, stop := range stops {
		row, err := s.Store.Get(ctx, id)
		if err != nil {
			continue
		}
		if row.State == StateCancelled {
			stop()
		}
	}
}

// claim starts every queued job, oldest first.
func (s *Service) claim(ctx context.Context) {
	queued, err := s.Store.ClaimQueued(ctx)
	if err != nil {
		s.logger()("jobs: read queued jobs: %v", err)
	}
	for _, job := range queued {
		s.start(job)
	}
}

// Cancel stops a queued or running job. A queued job never starts.
func (s *Service) Cancel(ctx context.Context, id string) (Job, error) {
	s.mu.Lock()
	stop := s.stops["job:"+id]
	s.mu.Unlock()
	if stop != nil {
		stop()
		return s.Store.Get(ctx, id)
	}
	// No live executor owns it: a queued row, or a running one this engine has
	// not picked up. The row ends inside one transaction that works on the row
	// as it lies: a start recorded in the meantime keeps its metadata, and the
	// report tells what the table holds. The worker of a queued row skips it.
	row, won, err := s.Store.Cancel(ctx, id, s.Store.now())
	if err != nil {
		return Job{}, err
	}
	if !won {
		return row, nil
	}
	if s.Reporter != nil && !row.Started.IsZero() {
		if err := s.Reporter.JobFinished(ctx, row); err != nil {
			s.logger()("jobs: report cancelled job %s: %v", row.ID, err)
		}
	}
	return row, nil
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
	if !reattach {
		job.State = StateRunning
		job.Started = s.Store.now()
		// The transition to running is conditional: a cancel that reached the
		// row between the claim and this write wins, the node never sees the
		// job, and the canceller's own report stands.
		if _, won, err := s.Store.SaveOpen(ctx, job); err != nil {
			s.logger()("jobs: record started job %s: %v", job.ID, err)
		} else if !won {
			if s.onAbandoned != nil {
				s.onAbandoned(job)
			}
			return
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
	if reattach {
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
	if !reattach && job.SHA != "" {
		cache := path.Join(NodeBase(machine.Name), RepoKey(job.Repo)+".git")
		if err := x.PushCommit(ctx, job.Repo, cache, job.SHA); err != nil {
			s.finish(&job, err)
			return
		}
	}
	spec := RunSpec{ID: job.ID, Dir: path.Join(job.Dir, "detached"), Unit: job.Unit, Attach: reattach, Offset: job.LogBytes}
	if !job.Host {
		limits := resources.Scope(false)
		spec.Limits = &limits
	}
	if !reattach {
		spec.Script = jobScript(machine, job, resources)
		if _, won, err := s.Store.SaveOpen(ctx, job); err != nil {
			s.logger()("jobs: record started job %s: %v", job.ID, err)
		} else if !won {
			// A cancel landed while the run prepared; the row keeps its end.
			if s.onAbandoned != nil {
				s.onAbandoned(job)
			}
			return
		}
		if s.Reporter != nil {
			if err := s.Reporter.JobStarted(ctx, job); err != nil {
				s.logger()("jobs: report started job %s: %v", job.ID, err)
			}
		}
	}

	poller := s.poller(conn)
	if poller == nil {
		return
	}
	// The node enforces the deadline itself; the engine allows the queue wait
	// and a slow route on top before it stops following the job.
	var timeoutCancel context.CancelFunc
	runCtx, timeoutCancel := context.WithTimeout(ctx, job.Timeout+AdmissionWait+2*time.Minute)
	defer timeoutCancel()
	runErr := poller.Run(runCtx, spec, logWriter)
	if runErr != nil && errors.Is(runErr, context.Canceled) && EngineStopping() {
		// The engine is stopping: the job keeps running on its node and the
		// restarted engine reattaches through this row. The offset write is
		// conditional too, so it cannot revive a row another engine ended.
		_, _, _ = s.Store.SaveOpen(context.WithoutCancel(ctx), logWriter.snapshot())
		return
	}
	if runErr != nil && !job.State.Finished() {
		s.stopUnit(x, spec)
	}
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
	job.State = state
	job.ExitCode = exit
	job.Ended = s.Store.now()
	ctx := context.Background()
	// The end is a conditional write: another engine may have cancelled the
	// row while this one held the executor. The row's own terminal state and
	// report then stay, and this engine stops the node process quietly.
	row, won, err := s.Store.SaveOpen(ctx, *job)
	if err != nil {
		s.logger()("jobs: record finished job %s: %v", job.ID, err)
	}
	if !won {
		job.State, job.ExitCode, job.Ended = row.State, row.ExitCode, row.Ended
	} else {
		if s.Reporter != nil {
			if err := s.Reporter.JobFinished(ctx, *job); err != nil {
				s.logger()("jobs: report finished job %s: %v", job.ID, err)
			}
		}
	}
	if job.Dir != "" && !EngineStopping() {
		machine, err := s.Machines.Get(ctx, job.Node)
		if err != nil {
			s.logger()("jobs: remove the folder of job %s: resolve machine %s: %v", job.ID, job.Node, err)
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

// machineFacts reads a machine's clamp facts. A machine with no CI slots
// still runs one job at a time, so the slot floor is one.
func machineFacts(m machines.Machine) Facts {
	slots := m.CISlots
	if slots < 1 {
		slots = 1
	}
	return Facts{Slots: slots, CPUs: m.Capabilities.CPUs, MemoryBytes: m.Capabilities.MemTotal}
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

// jobScript is the script a job runs on its node: it takes a CI slot, checks
// out the commit when the job has one, and runs the command under the job's
// deadline.
func jobScript(machine machines.Machine, job Job, resources Resources) string {
	q := shellquote.Quote
	base := NodeBase(machine.Name)
	work := path.Join(job.Dir, "work")
	seconds := int64((job.Timeout + time.Second - 1) / time.Second)
	inner := "set -euo pipefail; mkdir -p " + q(path.Dir(work)) + "; "
	if job.SHA != "" {
		cache := path.Join(base, RepoKey(job.Repo)+".git")
		inner += "flock " + q(cache+".worktrees.lock") + " git --git-dir=" + q(cache) + " worktree add --detach " + q(work) + " " + q(job.SHA) + " && "
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
		docker := "(docker)"
		if machine.SSH == "" {
			docker = "(docker); if ! docker info >/dev/null 2>&1; then docker=(sudo -n docker); fi"
		}
		inner += docker + "; "
		mount := ""
		if job.SHA != "" {
			// The checkout lives on the node, so the container mounts it and
			// works there, exactly as a CI check container does.
			mount = " -v " + q(work+":/workspace") + " -w /workspace"
		}
		inner += "timeout --signal=TERM --kill-after=10s " + strconv.FormatInt(seconds, 10) + "s \"${docker[@]}\" run --rm --init --cpus=" + strconv.Itoa(resources.CPUs) +
			" --memory=" + DockerMemory(resources.MemoryMiB) + " --memory-swap=" + DockerMemory(resources.MemoryMiB) +
			" --pids-limit=512" + mount + " " + q(job.Image) + " bash -o pipefail -c " + q(command)
	} else {
		if job.SHA != "" {
			inner += "cd " + q(work) + "; "
		}
		inner += "timeout --foreground --signal=TERM --kill-after=10s " + strconv.FormatInt(seconds, 10) + "s bash -o pipefail -c " + q(command)
	}
	slots := machine.CISlots
	if slots < 1 {
		slots = 1
	}
	return SlotScript(base, slots, 0, path.Join(job.Dir, ".active"), job.Timeout+AdmissionWait, inner)
}
