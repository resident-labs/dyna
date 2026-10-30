package ci

import (
	"time"

	"dyna-agent/internal/localci"
)

// shortHeadLength is how much of a head SHA names the commit to a reader. The
// PR holds the whole value.
const shortHeadLength = 7

// RunningKind names what a machine run belongs to. A client words the kind
// itself, so a run that has no pull request number still reads clearly.
type RunningKind string

const (
	// RunningPullRequest checks a pull request head.
	RunningPullRequest RunningKind = "pull-request"
	// RunningLocalChange checks a change on a local branch. Such a change has
	// no pull request number.
	RunningLocalChange RunningKind = "local-change"
	// RunningSync checks the integration branch after Dyna merges the default
	// branch into it.
	RunningSync RunningKind = "sync"
)

// Running is one local CI run in flight: the machine it runs on, the work it
// checks, and the suites it runs once they are known. It is a copy, so a reader
// never shares state with the scheduler.
type Running struct {
	// Machine is the name of the CI node, which is a machine with the CI role.
	Machine string `json:"machine"`
	Repo    string `json:"repo"`
	Number  int    `json:"number"`
	// Kind is the work the run checks, which a client words without a number.
	Kind RunningKind `json:"kind"`
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

// Running copies one run. The caller holds the run set's lock.
func (r *Run) Running() Running {
	head := r.Head
	if len(head) > shortHeadLength {
		head = head[:shortHeadLength]
	}
	return Running{
		Machine: r.Node, Repo: r.Repo, Number: r.Number,
		Kind: r.Kind, Branch: r.Branch, Trial: r.Trial, Head: head, Title: r.Title, URL: r.URL,
		Suites: localci.SuiteNames(r.Suites), Started: r.Started,
	}
}
