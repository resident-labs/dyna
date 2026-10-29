package websvc

import (
	"testing"
	"time"

	"dyna-agent/internal/machines"
)

// A CI machine reports the work its node queue holds now, and its page lists
// the runs with the PR each one checks. A run on a machine this registry does
// not list counts nowhere.
func TestMachineCIWorkAndRuns(t *testing.T) {
	dir := t.TempDir()
	if err := machines.Migrate(dir); err != nil {
		t.Fatal(err)
	}
	started := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	runs := []MachineCIRun{
		{Machine: "hermes", Number: 272, Title: "Show running local CI per machine", URL: "https://github.com/resident-labs/dyna/pull/272", Suites: []string{"go", "cross-build"}, Started: started},
		{Machine: "hermes", Number: 273, Title: "Probe machines and show their health", URL: "https://github.com/resident-labs/dyna/pull/273", Trial: true, Started: started.Add(time.Minute)},
		{Machine: "gone", Number: 9, Title: "A machine this registry dropped", Started: started},
	}
	api := MachineAPI{Store: machines.Store{DataDir: dir}, CI: func() []MachineCIRun { return runs }, CIQueue: func(node string) (int, int) {
		if node == "hermes" {
			return 2, 5
		}
		return 0, 0
	}}
	hermes, err := api.Store.Save(t.Context(), machines.Machine{Name: "hermes", SSH: "pip@hermes", Enabled: true, Labels: []string{machines.LabelCI}})
	if err != nil {
		t.Fatal(err)
	}

	list, err := api.Machines(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var listed MachineSummary
	for _, m := range list {
		if m.Name == "hermes" {
			listed = m
		}
	}
	if listed.CI == nil || listed.CI.Running != 2 || listed.CI.Waiting != 5 {
		t.Fatalf("hermes CI = %+v; want 2 running and 5 waiting", listed.CI)
	}

	detail, err := api.Machine(t.Context(), hermes.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.CIRuns) != 2 || detail.CIRuns[0].Title != runs[0].Title || detail.CIRuns[0].URL != runs[0].URL || !detail.CIRuns[1].Trial {
		t.Fatalf("hermes runs = %+v, want its two runs and the PRs they check", detail.CIRuns)
	}
	if got := detail.CIRuns[0].Suites; len(got) != 2 || got[0] != "go" {
		t.Fatalf("run suites = %v, want the suites the run prepared", got)
	}

	// An engine that runs no local CI reports no work for any machine.
	api.CI = nil
	api.CIQueue = nil
	quiet, err := api.Machine(t.Context(), hermes.ID)
	if err != nil {
		t.Fatal(err)
	}
	if quiet.CI == nil || quiet.CI.Running != 0 || quiet.CI.Waiting != 0 || len(quiet.CIRuns) != 0 {
		t.Fatalf("CI without a scheduler = %+v runs %+v, want no work", quiet.CI, quiet.CIRuns)
	}
}
