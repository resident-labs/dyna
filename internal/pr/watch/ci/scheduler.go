// Package ci owns the in-memory run registry and pure scheduling decisions for
// pull request local CI. Service orchestration remains in the watcher package.
package ci

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"dyna-agent/internal/jobs"
	"dyna-agent/internal/localci"
	"dyna-agent/internal/machines"
)

// Registry holds the saved results, machines and CI mode that scheduled runs use.
type Registry interface {
	Nodes(context.Context) ([]localci.Node, error)
	Mode(context.Context, string) (string, error)
	SaveResult(context.Context, localci.Result) error
	LoadResult(context.Context, string, string) (localci.Result, error)
	LoadHostedRuns(context.Context, string, string) (localci.HostedRuns, error)
	SaveHostedRuns(context.Context, string, string, localci.HostedRuns) error
}

// Runs owns the local CI runs in flight for one PR store.
type Runs struct {
	sync.Mutex
	// closed stops admission when the engine stops. It is atomic, so a stop
	// sets it without waiting for a tick that holds the run lock over its work.
	closed atomic.Bool
	Runs   map[string]*Run
}

// Has reports whether key already has a run. The caller holds the runs lock.
func (runs *Runs) Has(key string) bool {
	return runs.Runs[key] != nil
}

// Add admits run under key unless admission is closed or the key already has a
// run. The caller holds the runs lock.
func (runs *Runs) Add(key string, run *Run) bool {
	if runs.closed.Load() || runs.Has(key) {
		return false
	}
	runs.Runs[key] = run
	return true
}

// Finish removes key only when it still belongs to run. It takes the runs lock
// itself and reports whether it removed the run.
func (runs *Runs) Finish(key string, run *Run) bool {
	runs.Lock()
	defer runs.Unlock()
	if runs.Runs[key] != run {
		return false
	}
	delete(runs.Runs, key)
	return true
}

// CancelUnwanted cancels runs whose nodes no longer take CI and hosted runs
// that no desired job names. The caller holds the runs lock.
func (runs *Runs) CancelUnwanted(desired map[string]struct{}, enabled []localci.Node) {
	for key, run := range runs.Runs {
		ready := false
		for _, node := range enabled {
			ready = ready || node.Name == run.Node
		}
		_, wanted := desired[key]
		// A local head's run belongs to the trial or the sync operation that
		// asked for it, and the list of wanted hosted jobs never names it.
		if !ready || !run.Local && !wanted {
			run.Cancel()
		}
	}
}

// CloseAdmission stops new admissions and copies the runs admitted so far.
// Admission closes before this method waits for the run lock, which a tick
// holds over its work, so a finishing tick cannot add a run that the snapshot
// then misses.
func (runs *Runs) CloseAdmission() []StopTarget {
	runs.closed.Store(true)
	var targets []StopTarget
	runs.Lock()
	for _, run := range runs.Runs {
		targets = append(targets, StopTarget{Label: run.label(), Cancel: run.Cancel, Stopped: run.Stopped})
	}
	runs.Unlock()
	return targets
}

// Closed reports whether the run set refuses new admissions.
func (runs *Runs) Closed() bool {
	return runs.closed.Load()
}

// Run is one local CI run in flight. Node, Cancel and the lifecycle channels
// belong to the scheduler; the other fields describe the run to a reader.
type Run struct {
	Node   string
	Cancel context.CancelFunc
	// Stopped is closed when the run's work on the node has ended. An engine that
	// stops waits on it, because a run that outlives its engine leaves a keeper
	// on the machine that holds the cache lock for hours.
	Stopped  chan struct{}
	stopOnce sync.Once
	// Ended is closed after the run has left the run set and written its last
	// result or refresh, so a caller can wait for every write the run makes.
	Ended chan struct{}

	Repo   string
	Number int
	// Kind is the work the run checks, which the machine view words.
	Kind RunningKind
	// Branch names a local change's branch, which has no pull request number.
	Branch string
	Trial  bool
	// Local marks a run for a local head. No hosted poll decides that head: the
	// trial or the sync operation that admitted the run reads its own saved
	// result, and only the engine's stop cancels the run.
	Local   bool
	Head    string
	Title   string
	URL     string
	Started time.Time
	Suites  []localci.Suite
}

// MarkStopped records that the run has no work left on the node.
func (run *Run) MarkStopped() { run.stopOnce.Do(func() { close(run.Stopped) }) }

// label names one scheduled run in a log line: its head and its machine.
func (run *Run) label() string {
	return shortHead(run.Head) + " on " + run.Node
}

func shortHead(head string) string {
	if len(head) > shortHeadLength {
		return head[:shortHeadLength]
	}
	return head
}

// StopTarget is one local CI run the engine has to end: its cancel handle, the
// channel that closes when its work on the machine ends, and a name for a log line.
type StopTarget struct {
	Label   string
	Cancel  context.CancelFunc
	Stopped <-chan struct{}
}

// SavedResultAction says what admission does with a saved result that already
// covers a head.
type SavedResultAction int

const (
	// SettleSavedResult keeps the result: it tested the commit, or Dyna has no
	// reason to run the head again.
	SettleSavedResult SavedResultAction = iota
	// RetryOnAnotherMachine moves a machine-level failure to another machine.
	RetryOnAnotherMachine
	// RetryStoppedRun runs a head again because Dyna itself stopped its run. That
	// says nothing about the commit, so the same machine may take it.
	RetryStoppedRun
)

// SavedResultActionFor picks what to do with a complete saved result of a head
// whose local run is the only provider, such as a merge trial.
func SavedResultActionFor(result localci.Result) SavedResultAction {
	if result.Resumed {
		// This result is already the replacement for a run that proved nothing, so
		// its retry is spent whatever failure it hit. Settling here keeps admission
		// and the gate agreeing instead of one refusing to run and the other
		// waiting for a retry that will never start.
		return SettleSavedResult
	}
	if MachineFailureRetryFrom(result) != "" {
		return RetryOnAnotherMachine
	}
	if result.Cause == localci.CauseScheduling && !result.Resumed {
		// Preparing the run failed, so no check ran. That is a scheduling
		// failure, not evidence about the commit, so it runs once more.
		return RetryStoppedRun
	}
	if StoppedLocalResult(result) && !result.Resumed {
		return RetryStoppedRun
	}
	return SettleSavedResult
}

// LocalInfrastructureError reports a result with no conclusive test evidence.
func LocalInfrastructureError(result localci.Result) bool {
	if result.Status == localci.OutcomeError {
		return true
	}
	for _, check := range result.Checks {
		if check.Status == localci.OutcomeError {
			return true
		}
	}
	return false
}

// StoppedLocalResult reports a local result that Dyna itself stopped: an engine
// stop or a cancelled run. Every check in such a result may carry the cause, so
// one is enough.
func StoppedLocalResult(result localci.Result) bool {
	if !LocalInfrastructureError(result) {
		return false
	}
	if stoppedCause(result.Cause) {
		return true
	}
	for _, check := range result.Checks {
		// A group's row is derived from its members, so it has no cause of its own.
		if !check.Aggregate && stoppedCause(check.Cause) {
			return true
		}
	}
	return false
}

// stoppedCause reports a cause that names Dyna stopping the run, not the commit
// and not the machine.
func stoppedCause(cause localci.FailureCause) bool {
	return cause == localci.CauseEngineStop || cause == localci.CauseCancelled
}

// MachineFailureRetryFrom names the machine whose failure made the result, when
// the result is a machine-level infrastructure failure that has not been retried
// yet. An empty string means the result stands: it either tested the commit or
// it is already the retry of another machine's failure.
func MachineFailureRetryFrom(result localci.Result) string {
	if result.Node == "" || result.RetriedFrom != "" || !LocalInfrastructureError(result) {
		return ""
	}
	if result.Cause != "" && !machineCause(result.Cause) {
		return ""
	}
	failed := false
	for _, check := range result.Checks {
		// A group's row is derived from its members, so it has no cause of its
		// own. Only a member says why the group failed.
		if check.Aggregate {
			continue
		}
		if check.Status == localci.OutcomePassed {
			continue
		}
		if !machineCause(check.Cause) {
			return ""
		}
		failed = true
	}
	if !failed && !machineCause(result.Cause) {
		return ""
	}
	return result.Node
}

// machineCause reports a failure the machine caused, which says nothing about
// the commit. An unclassified failure is one where the check proved no test
// result at all, such as a scope that the node's systemd refused or a container
// that never ran; only a check that proves a test failure is evidence about the
// commit.
func machineCause(cause localci.FailureCause) bool {
	switch cause {
	case localci.CauseContainerFailure, localci.CauseSSHDrop, localci.CauseDiskFull, localci.CauseUnclassified, localci.CauseGit:
		return true
	}
	return false
}

// SavedLocalResultKey identifies one saved local result, so the watcher can tell
// a result the PR has already read from one a manual rerun just wrote.
func SavedLocalResultKey(result localci.Result) string {
	body, err := json.Marshal(result)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:])
}

// AvailableCINode prefers the node with the most room in the shared node
// queue: the CPUs it keeps free after the work it holds and the work that
// waits for it. A machine that names rooms takes only those rooms' work. A
// machine whose disk rule blocks admission counts as having no room and never
// wins against a healthy machine, whatever their capacity; only a fleet with
// no healthy machine takes one, where the queued work waits behind the rule.
// skip names the machine a run just
// failed on, so a retry lands somewhere else. A floor below zero also returns
// a node whose queue is full, so work that must join the node queue, a merge
// trial, starts on the roomiest node there is and waits there.
// attachedCINodes names the machine that still holds checks of an earlier engine
// for a commit.
type attachedCINodes interface {
	AttachedNode(ctx context.Context, repo, sha string) (string, error)
}

// PinnedCINode pins a run to the machine that still runs its checks from before
// an engine restart. Those checks already hold the machine's queue rows, so
// the machine takes the run back; any other machine would start the checks
// again and abandon the first ones. A machine whose disk is full takes the run
// too, and its checks wait in its queue like any other work. It returns no pin
// when there is no record, when the run just failed there, or when the machine
// is no longer registered, enabled or allowed for the room.
func PinnedCINode(ctx context.Context, registry Registry, repo, head string, nodes []localci.Node, roomID, skip string) *localci.Node {
	finder, ok := registry.(attachedCINodes)
	if !ok {
		return nil
	}
	name, err := finder.AttachedNode(ctx, repo, head)
	if err != nil || name == "" || name == skip {
		return nil
	}
	for i := range nodes {
		node := &nodes[i]
		if node.Name != name || !node.Enabled || !node.AllowsRoom(roomID) {
			continue
		}
		return node
	}
	return nil
}

func AvailableCINode(nodes []localci.Node, roomID, skip string, floor int, free func(node localci.Node) int) *localci.Node {
	var chosen *localci.Node
	chosenBlocked := false
	roomiest := floor
	for i := range nodes {
		node := &nodes[i]
		if !node.AllowsRoom(roomID) || node.Name == skip || !node.Enabled {
			continue
		}
		available := free(*node)
		// A disk-blocked node admits no work: its queue holds every check
		// behind the disk rule, so it counts as no room however free its CPUs
		// read. Health outranks room, so a blocked node never wins against a
		// healthy one, whatever their capacity: only work that joins a queue
		// whatever happens, a merge trial on a fleet with no healthy node,
		// still takes a blocked node and waits there with the visible reason.
		blocked := (machines.Machine{MaxDiskPercent: node.MaxDiskPercent, Capabilities: machines.Capabilities{Disk: node.Disk}}).DiskBlock() != ""
		if blocked {
			available = 0
		}
		better := available > roomiest
		if chosen != nil && chosenBlocked != blocked {
			better = !blocked
		}
		if better {
			chosen, chosenBlocked, roomiest = node, blocked, available
		}
	}
	return chosen
}

// CheckKind words the kind of a run for its queue rows, which say what each
// of its checks tests. An unknown kind reads as a pull request, the only kind
// this engine made before local heads and syncs reached the queue.
func CheckKind(kind RunningKind) jobs.CheckKind {
	switch kind {
	case RunningLocalChange:
		return jobs.CheckKindLocalChange
	case RunningSync:
		return jobs.CheckKindSync
	}
	return jobs.CheckKindPullRequest
}

// MissingRemoteRef reports git saying the ref it was asked for is not published
// yet. Every other fetch failure is a real scheduling failure.
func MissingRemoteRef(detail string) bool {
	detail = strings.ToLower(detail)
	return strings.Contains(detail, "couldn't find remote ref") || strings.Contains(detail, "could not find remote ref")
}
