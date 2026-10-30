package watch

import (
	"slices"
	"sort"
	"sync"

	"dyna-agent/internal/localci"
	cisched "dyna-agent/internal/pr/watch/ci"
)

// RunningCIKind names what a machine run belongs to. A client words the kind
// itself, so a run that has no pull request number still reads clearly.
type RunningCIKind = cisched.RunningKind

const (
	// RunningCIPullRequest checks a pull request head.
	RunningCIPullRequest = cisched.RunningPullRequest
	// RunningCILocalChange checks a change on a local branch. Such a change has
	// no pull request number.
	RunningCILocalChange = cisched.RunningLocalChange
	// RunningCISync checks the integration branch after Dyna merges the default
	// branch into it.
	RunningCISync = cisched.RunningSync
)

// RunningCI is one local CI run in flight. It is a copy, so a reader never
// shares state with the scheduler.
type RunningCI = cisched.Running

// runningCISets holds this process's run sets by PR store root. A run lives in
// memory for one process, so every service over the same store sees the same
// set: the engine's workers own the runs, and a service that only reads
// reports them.
var (
	runningCISetsMu sync.Mutex
	runningCISets   = map[string]*registeredCIRuns{}
)

func runningCISet(root string) *registeredCIRuns {
	runningCISetsMu.Lock()
	defer runningCISetsMu.Unlock()
	runs := runningCISets[root]
	if runs == nil {
		runs = &registeredCIRuns{Runs: map[string]*registeredCIRun{}}
		runningCISets[root] = runs
	}
	return runs
}

// RunningCI copies the local CI runs in flight, so a client can show what each
// machine is doing. It holds the run lock only while it copies.
func (s *Service) RunningCI() []RunningCI {
	runs := s.nodeRuns()
	runs.Lock()
	list := make([]RunningCI, 0, len(runs.Runs))
	for _, run := range runs.Runs {
		list = append(list, run.Running())
	}
	runs.Unlock()
	sort.Slice(list, func(i, j int) bool {
		if !list[i].Started.Equal(list[j].Started) {
			return list[i].Started.Before(list[j].Started)
		}
		if list[i].Machine != list[j].Machine {
			return list[i].Machine < list[j].Machine
		}
		return list[i].Head < list[j].Head
	})
	return list
}

// setRunningCISuites records the suites a run started, so a reader can name
// them while the run is in flight.
func (s *Service) setRunningCISuites(key string, run *registeredCIRun, suites []localci.Suite) {
	runs := s.nodeRuns()
	runs.Lock()
	if runs.Runs[key] == run {
		run.Suites = slices.Clone(suites)
	}
	runs.Unlock()
	s.runningCIChanged()
}

// runningCIChanged tells clients that the runs in flight changed, so they read
// the machine view again instead of polling it.
func (s *Service) runningCIChanged() {
	if s.RunningCIChanged != nil {
		s.RunningCIChanged()
	}
}
