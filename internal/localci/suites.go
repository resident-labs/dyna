package localci

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
	SuiteCrossBuild    Suite = "cross-build"
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
