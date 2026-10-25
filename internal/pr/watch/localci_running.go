package watch

import (
	"slices"
	"sort"
	"sync"
	"time"

	"dyna-agent/internal/localci"
)

// shortHeadLength is how much of a head SHA names the commit to a reader. The
// PR holds the whole value.
const shortHeadLength = 7

// RunningCIKind names what a machine run belongs to. A client words the kind
// itself, so a run that has no pull request number still reads clearly.
type RunningCIKind string

const (
	// RunningCIPullRequest checks a pull request head.
	RunningCIPullRequest RunningCIKind = "pull-request"
	// RunningCILocalChange checks a change on a local branch. Such a change has
	// no pull request number.
	RunningCILocalChange RunningCIKind = "local-change"
	// RunningCISync checks the integration branch after Dyna merges the default
	// branch into it.
	RunningCISync RunningCIKind = "sync"
)

// RunningCI is one local CI run in flight: the machine it holds slots on, the
// work it checks, and the suites it runs once they are known. It is a copy, so
// a reader never shares state with the scheduler.
type RunningCI struct {
	// Machine is the name of the CI node, which is a machine with the CI role.
	Machine string `json:"machine"`
	// Slots is how many of the machine's CI slots the run reserved.
	Slots  int    `json:"slots"`
	Repo   string `json:"repo"`
	Number int    `json:"number"`
	// Kind is the work the run checks, which a client words without a number.
	Kind RunningCIKind `json:"kind"`
	// Branch names the branch of a local change, which has no pull request.
	Branch string `json:"branch,omitempty"`
	// Trial marks a merge trial, which checks a merge of several heads.
	Trial bool `json:"trial"`
	// Head is the short head SHA the run checks.
	Head string `json:"head"`
	// Title and URL name the PR the run checks, so a client shows the PR and
	// not only its number. A merge trial carries the PR its batch started from.
	Title string `json:"title"`
	URL   string `json:"url"`
	// Suites is empty until the run has prepared its suites.
	Suites  []string  `json:"suites,omitempty"`
	Started time.Time `json:"started"`
}

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
		runs = &registeredCIRuns{runs: map[string]*registeredCIRun{}}
		runningCISets[root] = runs
	}
	return runs
}

// RunningCI copies the local CI runs in flight, so a client can show what each
// machine is doing. It holds the run lock only while it copies.
func (s *Service) RunningCI() []RunningCI {
	runs := s.nodeRuns()
	runs.Lock()
	list := make([]RunningCI, 0, len(runs.runs))
	for _, run := range runs.runs {
		list = append(list, run.runningCI())
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

// runningCI copies one run. The caller holds the run set's lock.
func (r *registeredCIRun) runningCI() RunningCI {
	head := r.head
	if len(head) > shortHeadLength {
		head = head[:shortHeadLength]
	}
	return RunningCI{
		Machine: r.node, Slots: r.slots, Repo: r.repo, Number: r.number,
		Kind: r.kind, Branch: r.branch, Trial: r.trial, Head: head, Title: r.title, URL: r.url,
		Suites: localci.SuiteNames(r.suites), Started: r.started,
	}
}

// setRunningCISuites records the suites a run started, so a reader can name
// them while the run is in flight.
func (s *Service) setRunningCISuites(key string, run *registeredCIRun, suites []localci.Suite) {
	runs := s.nodeRuns()
	runs.Lock()
	if runs.runs[key] == run {
		run.suites = slices.Clone(suites)
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
