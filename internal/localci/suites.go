package localci

import (
	"fmt"
	"strings"
)

// Suite names one suite or check of a repository CI graph. A saved result stores
// the value, so its spelling must not change during a definition migration.
type Suite string

const (
	// These stable identities name Dyna's own checks in old results and tests.
	// Repository definitions, not these constants, drive selection and execution.
	SuiteRelay         Suite = "relay"
	SuiteFrontend      Suite = "frontend"
	SuiteFrontendCheck Suite = "frontend-check"
	SuiteGo            Suite = "go"
	SuiteGoTest        Suite = "go-test"
	SuiteGoStatic      Suite = "go-static"
	// SuiteCross is the per-target cross-build suite. Its group and the name
	// users pass to `git ci run --suite` stay cross-build, which is also the
	// aggregate check name a base requires.
	SuiteCross         Suite = "cross"
	SuiteComputerSmoke Suite = "computer-smoke"
	// SuiteChanges is the placeholder for a diff that needs no suite reports.
	SuiteChanges Suite = "changes"
)

// Group is an aggregate result that repeats the outcome of every check feeding
// it. Callers require and show the group instead of the checks behind it.
type Group string

// CheckName is the name one check reports under: in a saved result, on the merge
// card and at the provider. The spelling is stored, so it must not change.
type CheckName string

// Name is the name an aggregate reports under, which is also how the group is
// spelled.
func (g Group) Name() CheckName { return CheckName(g) }

// Check is one check a suite expands into.
type Check struct {
	// ID is the check's id on a node; a saved result stores it as the suite.
	ID Suite
	// Name is the name the check reports under.
	Name CheckName
	// Group is the aggregate the check feeds, empty when it reports itself.
	Group Group
}

// SuiteNames converts suites for a stored or wire field.
func SuiteNames(suites []Suite) []string {
	out := make([]string, 0, len(suites))
	for _, suite := range suites {
		out = append(out, string(suite))
	}
	return out
}

func containsName(names []CheckName, want CheckName) bool {
	for _, name := range names {
		if name == want {
			return true
		}
	}
	return false
}

// Complete reports a saved result that describes a finished run: a node ran it,
// and it holds either a decided outcome or an infrastructure error.
func (r Result) Complete() bool {
	return r.Node != "" && (r.Status == OutcomeError || len(r.Checks) > 0 && r.Status.Decided())
}

// SuiteBlockedError names a suite that a local run needs and no machine can
// take. A run that waits for such a machine waits for ever, so the trial that
// asked for it fails with this error instead.
type SuiteBlockedError struct {
	// Suite is a suite that no enabled machine allows. It is empty when each
	// suite is allowed somewhere, but no one machine allows them all.
	Suite Suite
	// Suites is every suite the run needs, so the message can name the set.
	Suites []Suite
}

func (e SuiteBlockedError) Error() string {
	if e.Suite != "" {
		return fmt.Sprintf("no enabled CI machine allows the %s suite that this run needs; allow it with dyna machine edit <name> --suites %s", e.Suite, e.Suite)
	}
	return fmt.Sprintf("no enabled CI machine allows every suite that this run needs (%s); allow them on one machine with dyna machine edit <name> --suites %s",
		strings.Join(SuiteNames(e.Suites), ", "), strings.Join(SuiteNames(e.Suites), ","))
}

// NodesForSuites keeps the machines that allow every suite of a run. A machine
// that names no suite allows every suite, so it stays.
func NodesForSuites(nodes []Node, suites []Suite) []Node {
	kept := make([]Node, 0, len(nodes))
	for _, node := range nodes {
		if allowsSuites(node, suites) {
			kept = append(kept, node)
		}
	}
	return kept
}

// BlockedSuite reports why no machine can take a run with these suites. It
// returns nil when at least one machine allows every suite, or when the run
// names no suite.
func BlockedSuite(nodes []Node, suites []Suite) error {
	if len(NodesForSuites(nodes, suites)) > 0 {
		return nil
	}
	for _, suite := range suites {
		allowed := false
		for _, node := range nodes {
			allowed = allowed || node.AllowsSuite(suite)
		}
		if !allowed {
			return SuiteBlockedError{Suite: suite, Suites: suites}
		}
	}
	return SuiteBlockedError{Suites: suites}
}

// allowsSuites reports whether the machine allows every suite of the run.
func allowsSuites(node Node, suites []Suite) bool {
	for _, suite := range suites {
		if !node.AllowsSuite(suite) {
			return false
		}
	}
	return true
}
