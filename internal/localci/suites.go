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
		return fmt.Sprintf("no enabled CI node allows the %s suite that this run needs; allow it with dyna node edit <name> --label suite=%s", e.Suite, e.Suite)
	}
	flags := make([]string, 0, len(e.Suites))
	for _, suite := range e.Suites {
		flags = append(flags, "--label suite="+string(suite))
	}
	return fmt.Sprintf("no enabled CI node allows every suite that this run needs (%s); allow them on one node with dyna node edit <name> %s",
		strings.Join(SuiteNames(e.Suites), ", "), strings.Join(flags, " "))
}

// RoomNotServedError names a room that no enabled CI machine serves. The
// scheduler never starts a run for such a room, so a head that waits for local
// CI waits for ever; the head that asked for it fails with this error instead.
type RoomNotServedError struct {
	RoomID string
}

func (e RoomNotServedError) Error() string {
	return fmt.Sprintf("no enabled CI node serves room %s; an owner adds the room to a CI node with dyna node edit <name> --rooms %s, or on the Nodes page; then run dyna git merge retry", e.RoomID, e.RoomID)
}

// NodesForRoom keeps the machines that take CI work for the room. A machine
// that names no room takes work from every room, so it stays.
func NodesForRoom(nodes []Node, roomID string) []Node {
	kept := make([]Node, 0, len(nodes))
	for _, node := range nodes {
		if node.AllowsRoom(roomID) {
			kept = append(kept, node)
		}
	}
	return kept
}

// NodesForSuites keeps the machines that allow every suite of a run. A machine
// without suite= labels allows every suite; imported labels restrict the rest.
func NodesForSuites(nodes []Node, suites []Suite) []Node {
	kept := make([]Node, 0, len(nodes))
	for _, node := range nodes {
		if allowsSuites(node, suites) {
			kept = append(kept, node)
		}
	}
	return kept
}

// BlockedSuite reports why no machine can take a run with these suites. Pass
// only the machines that may take the run at all, such as those of its room: a
// machine another room owns cannot take the suite either. It returns nil when
// at least one machine allows every suite, or when the run names no suite. A
// machine that is busy or low on disk still allows a suite, so a caller must
// keep those in the list.
func BlockedSuite(nodes []Node, suites []Suite) error {
	if len(NodesForSuites(nodes, suites)) > 0 {
		return nil
	}
	for _, suite := range suites {
		allowed := false
		for _, node := range nodes {
			allowed = allowed || allowsSuite(node, suite)
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
		if !allowsSuite(node, suite) {
			return false
		}
	}
	return true
}

// allowsSuite preserves the old filter with owner labels: no suite= label means
// every suite, while one or more labels limit the node to the suites they name.
func allowsSuite(node Node, suite Suite) bool {
	restricted := false
	for _, label := range node.Labels {
		if !strings.HasPrefix(label, "suite=") {
			continue
		}
		restricted = true
		if label == "suite="+string(suite) {
			return true
		}
	}
	return !restricted
}

// NodesForLabels keeps nodes that have every label a check run needs.
func NodesForLabels(nodes []Node, labels []string) []Node {
	kept := make([]Node, 0, len(nodes))
	for _, node := range nodes {
		matches := true
		for _, label := range labels {
			matches = matches && node.HasLabel(label)
		}
		if matches {
			kept = append(kept, node)
		}
	}
	return kept
}

// LabelsBlockedError names the check labels no one machine carries together.
type LabelsBlockedError struct{ Labels []string }

func (e LabelsBlockedError) Error() string {
	return fmt.Sprintf("no enabled CI node has every check label this run needs (%s); add the missing labels with dyna node edit <name> --label <label>", strings.Join(e.Labels, ", "))
}

// BlockedLabels reports a label set that no machine can satisfy. Busy machines
// still count, so their work waits for capacity instead of failing.
func BlockedLabels(nodes []Node, labels []string) error {
	if len(labels) == 0 || len(NodesForLabels(nodes, labels)) > 0 {
		return nil
	}
	return LabelsBlockedError{Labels: labels}
}
