package websvc

import (
	"testing"
	"time"

	"dyna-agent/internal/machines"
)

// A CI machine reports the slots its runs hold now out of its own slots, and
// its page lists the runs with the PR each one checks. A run on a machine this
// registry does not list counts nowhere.
func TestMachineCISlotUseAndRuns(t *testing.T) {
	dir := t.TempDir()
	if err := machines.Migrate(dir); err != nil {
		t.Fatal(err)
	}
	started := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	runs := []MachineCIRun{
		{Machine: "hermes", Number: 272, Title: "Show running local CI per machine", URL: "https://github.com/resident-labs/dyna/pull/272", Slots: 6, Suites: []string{"go", "cross-build"}, Started: started},
		{Machine: "hermes", Number: 273, Title: "Probe machines and show their health", URL: "https://github.com/resident-labs/dyna/pull/273", Slots: 6, Trial: true, Started: started.Add(time.Minute)},
		{Machine: "gone", Number: 9, Title: "A machine this registry dropped", Slots: 4, Started: started},
	}
	api := MachineAPI{Store: machines.Store{DataDir: dir}, CI: func() []MachineCIRun { return runs }}
	hermes, err := api.Store.Save(t.Context(), machines.Machine{Name: "hermes", Roles: []machines.Role{machines.RoleCI}, SSH: "pip@hermes", CISlots: 16, Enabled: true})
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
	if listed.CI == nil || listed.CI.Used != 12 || listed.CI.Slots != 16 || listed.CISlots != 16 {
		t.Fatalf("hermes CI = %+v, slots %d; want 12 of 16 used", listed.CI, listed.CISlots)
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

	// The engine host has no CI role, so it keeps no slot figure. An engine that
	// runs no local CI reports no use for any machine.
	for _, m := range list {
		if m.Local && m.CI != nil {
			t.Fatalf("local CI = %+v, want no figure without the ci role", m.CI)
		}
	}
	api.CI = nil
	quiet, err := api.Machine(t.Context(), hermes.ID)
	if err != nil {
		t.Fatal(err)
	}
	if quiet.CI == nil || quiet.CI.Used != 0 || len(quiet.CIRuns) != 0 {
		t.Fatalf("CI without a scheduler = %+v runs %+v, want no use", quiet.CI, quiet.CIRuns)
	}
}
