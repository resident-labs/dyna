package localci

import (
	"fmt"
	"strings"
)

// ShardCount is how many shards the go suite splits its tests into. The runner
// starts one check per shard, and a complete result holds every one of them.
const ShardCount = 8

// Suite names one suite or check of the shared CI graph. A saved result stores
// the value, so its spelling must not change.
type Suite string

const (
	SuiteRelay         Suite = "relay"
	SuiteFrontend      Suite = "frontend"
	SuiteFrontendCheck Suite = "frontend-check"
	SuiteGo            Suite = "go"
	SuiteGoTest        Suite = "go-test"
	SuiteGoStatic      Suite = "go-static"
	SuiteCrossBuild    Suite = "cross-build"
	SuiteComputerSmoke Suite = "computer-smoke"
	// SuiteChanges is the placeholder a diff that needs no suite reports.
	SuiteChanges Suite = "changes"
)

// Group is an aggregate result that repeats the outcome of every check feeding
// it. Callers require and show the group instead of the checks behind it.
type Group string

const (
	// GroupGoTest is the go suite's aggregate check.
	GroupGoTest Group = "go-test"
	// GroupGoTestShard is the group name a provider gives every go-test shard.
	GroupGoTestShard Group = "go-test-shard"
)

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

// goSuiteChecks is the go suite's expansion, in run order: the checks its
// aggregate repeats and the merge gate requires.
func goSuiteChecks() []Check {
	checks := []Check{
		{ID: SuiteFrontend, Name: "frontend", Group: GroupGoTest},
		{ID: SuiteFrontendCheck, Name: "frontend-check", Group: GroupGoTest},
		{ID: SuiteGoStatic, Name: "go-static", Group: GroupGoTest},
	}
	for i := 0; i < ShardCount; i++ {
		checks = append(checks, Check{ID: shardID(i), Name: shardName(i), Group: GroupGoTest})
	}
	return checks
}

// describedSuites is the one description of the suite graph. Every reader of a
// suite or check identity reads it: the runner, trial selection, saved-result
// completeness and the merge gate.
var describedSuites = []struct {
	suite  Suite
	checks []Check
}{
	{SuiteRelay, []Check{{ID: SuiteRelay, Name: "relay"}}},
	{SuiteFrontend, []Check{{ID: SuiteFrontend, Name: "frontend"}}},
	{SuiteFrontendCheck, []Check{{ID: SuiteFrontendCheck, Name: "frontend-check"}}},
	{SuiteGoStatic, []Check{{ID: SuiteGoStatic, Name: "go-static"}}},
	{SuiteCrossBuild, []Check{{ID: SuiteCrossBuild, Name: "cross-build"}}},
	{SuiteComputerSmoke, []Check{{ID: SuiteComputerSmoke, Name: "computer-smoke"}}},
	{SuiteGo, goSuiteChecks()},
}

var (
	checksBySuite = describe()
	// groupOfCheck maps a check's reported name and its id to its aggregate.
	groupOfCheck = describeGroups()
)

func describe() map[Suite][]Check {
	out := make(map[Suite][]Check, len(describedSuites))
	for _, entry := range describedSuites {
		out[entry.suite] = entry.checks
	}
	return out
}

func describeGroups() map[CheckName]Group {
	out := map[CheckName]Group{}
	for _, entry := range describedSuites {
		for _, check := range entry.checks {
			if check.Group == "" {
				continue
			}
			out[check.Name] = check.Group
			out[CheckName(check.ID)] = check.Group
		}
	}
	return out
}

// Suites returns every suite in the description, in run order.
func Suites() []Suite {
	out := make([]Suite, 0, len(describedSuites))
	for _, entry := range describedSuites {
		out = append(out, entry.suite)
	}
	return out
}

// Checks returns the checks a suite expands into, in run order. The go-test
// aggregate is spelled like the go suite's group, and a node run may name it in
// place of that suite.
func Checks(suite Suite) ([]Check, bool) {
	if suite == SuiteGoTest {
		suite = SuiteGo
	}
	checks, ok := checksBySuite[suite]
	if !ok {
		return nil, false
	}
	return append([]Check(nil), checks...), true
}

// GroupOf reports the aggregate a reported check feeds, or "" when it reports
// on its own.
func GroupOf(check CheckName) Group { return groupOfCheck[check] }

// ProviderGroup reports the group name a provider gives one check: every shard
// reports under GroupGoTestShard, and every other check under its own name.
func ProviderGroup(check CheckName) CheckName {
	if _, shard := shardOfName(check); shard {
		return GroupGoTestShard.Name()
	}
	return check
}

// ProviderGroups reports the distinct group names a provider reports the
// aggregate's checks under, in run order.
func ProviderGroups(group Group) []CheckName {
	var names []CheckName
	for _, check := range describedChecks() {
		if check.Group != group {
			continue
		}
		name := ProviderGroup(check.Name)
		if !containsName(names, name) {
			names = append(names, name)
		}
	}
	return names
}

func describedChecks() []Check {
	var out []Check
	for _, entry := range describedSuites {
		out = append(out, entry.checks...)
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

// DefaultSuites returns the complete suite set used for a trial run.
func DefaultSuites() []Suite {
	return []Suite{SuiteRelay, SuiteFrontend, SuiteGo, SuiteCrossBuild}
}

// WorkflowSuites includes the conditional jobs needed for a complete trial.
// DefaultSuites retains the manual SSH lane's historical suite selection.
func WorkflowSuites() []Suite {
	return []Suite{SuiteRelay, SuiteGo, SuiteCrossBuild, SuiteComputerSmoke}
}

// SuitesForFiles returns the suites a changed file set needs. Trial selection and
// saved-result completeness both read it, so a change runs what a change needs.
func SuitesForFiles(files []string) []Suite {
	relay, goSuite, computers := false, false, false
	for _, f := range files {
		workflow := strings.HasPrefix(f, ".github/")
		script := f == "scripts/ci.sh"
		relay = relay || workflow || script || strings.HasPrefix(f, "relay/")
		goSuite = goSuite || workflow || script || strings.HasSuffix(f, ".go") || strings.HasPrefix(f, "internal/") || f == "go.mod" || f == "go.sum" || strings.HasPrefix(f, "desktop/") || strings.HasPrefix(f, "web/") || f == "install.sh"
		computers = computers || workflow || strings.HasPrefix(f, "internal/computers/") || strings.HasPrefix(f, "internal/computerweb/")
	}
	var suites []Suite
	if relay {
		suites = append(suites, SuiteRelay)
	}
	if goSuite {
		suites = append(suites, SuiteGo, SuiteCrossBuild)
	}
	if computers {
		suites = append(suites, SuiteComputerSmoke)
	}
	return suites
}

// SuiteNames converts suites for a stored or wire field. The identity itself is
// never free text; only a reader that must show it as one converts here.
func SuiteNames(suites []Suite) []string {
	out := make([]string, 0, len(suites))
	for _, suite := range suites {
		out = append(out, string(suite))
	}
	return out
}

// ParseSuites converts suite names from a command line. It rejects a name the
// description does not know, so a typed typo never starts a run.
func ParseSuites(names []string) ([]Suite, error) {
	suites := make([]Suite, 0, len(names))
	for _, name := range names {
		if _, ok := Checks(Suite(name)); !ok {
			return nil, fmt.Errorf("unknown CI suite %q", name)
		}
		suites = append(suites, Suite(name))
	}
	return suites, nil
}

// RequiredChecks returns the check names a passing result must hold for the
// suites: every check each suite expands into, plus the aggregates they feed.
func RequiredChecks(suites []Suite) []CheckName {
	var required []CheckName
	add := func(name CheckName) {
		if name != "" && !containsName(required, name) {
			required = append(required, name)
		}
	}
	for _, suite := range suites {
		checks, ok := Checks(suite)
		if !ok {
			add(CheckName(suite))
			continue
		}
		for _, check := range checks {
			add(check.Name)
			add(check.Group.Name())
		}
	}
	if len(required) == 0 {
		add(CheckName(SuiteChanges))
	}
	return required
}

// Complete reports a saved result that describes a finished run: a node ran it,
// and it holds either a decided outcome or an infrastructure error.
func (r Result) Complete() bool {
	return r.Node != "" && (r.Status == OutcomeError || len(r.Checks) > 0 && r.Status.Decided())
}

// CoversSuites reports whether a result holds every check the suites require, so
// a partial or older result cannot pass for a complete one.
func CoversSuites(result Result, suites []Suite) bool {
	if !result.Complete() {
		return false
	}
	if result.Status != OutcomePassed {
		return true
	}
	seen := map[CheckName]bool{}
	for _, check := range result.Checks {
		seen[check.Name()] = check.Status == OutcomePassed
	}
	for _, name := range RequiredChecks(suites) {
		if !seen[name] {
			return false
		}
	}
	return true
}

func shardID(index int) Suite { return Suite(fmt.Sprintf("go-test-shard-%d", index)) }

func shardName(index int) CheckName {
	return CheckName(fmt.Sprintf("%s (%d)", GroupGoTestShard, index))
}

// IsShard reports whether a check is one of the go-test shards.
func IsShard(check Suite) bool {
	_, shard := ShardOf(check)
	return shard
}

// ShardOf reports the shard index of a check that is a go-test shard.
func ShardOf(check Suite) (int, bool) {
	for i := 0; i < ShardCount; i++ {
		if shardID(i) == check {
			return i, true
		}
	}
	return 0, false
}

func shardOfName(name CheckName) (int, bool) {
	for i := 0; i < ShardCount; i++ {
		if shardName(i) == name {
			return i, true
		}
	}
	return 0, false
}
