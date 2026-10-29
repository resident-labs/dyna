package subagent

import (
	"dyna-agent/internal/profile"
	"fmt"
	"strings"
	"testing"
)

func TestProfileListSortsByIntelligenceThenDiagnosticsAndCapsAtTen(t *testing.T) {
	profs := []profile.Profile{
		{Name: "low-b", Harness: profile.HarnessMock, Intelligence: 2, Diagnostics: 10},
		{Name: "beta", Harness: profile.HarnessMock, Intelligence: 9, Diagnostics: 5},
		{Name: "gamma", Harness: profile.HarnessMock, Intelligence: 9, Diagnostics: 8},
		{Name: "alpha", Harness: profile.HarnessMock, Intelligence: 9, Diagnostics: 8},
	}
	for i := 1; i <= 8; i++ {
		profs = append(profs, profile.Profile{Name: fmt.Sprintf("f%d", i), Harness: profile.HarnessMock, Intelligence: 5, Diagnostics: 1})
	}
	e := newTestEnv(t, profs...)
	items := e.m.profileInstructions(Meta{ID: "ses_20261019-000002-bbbbbbbb", Name: "reader", Room: e.room.Name, RoomID: e.room.ID, Depth: 1}).Items
	want := []string{"alpha", "gamma", "beta", "f1", "f2", "f3", "f4", "f5", "f6", "f7"}
	if len(items) != len(want)+1 {
		t.Fatalf("got %d items, want %d profiles and a remaining line", len(items), len(want))
	}
	for i, name := range want {
		if !strings.HasPrefix(items[i], "- "+name+" |") {
			t.Errorf("item %d = %q, want profile %s", i, items[i], name)
		}
	}
	if got, want := items[len(want)], "- 2 more enabled profiles: run `dyna profiles list` for all."; got != want {
		t.Errorf("remaining line = %q, want %q", got, want)
	}
}

func TestProfilesAppearInRootAndWorkerInstructions(t *testing.T) {
	enabled := profile.Profile{Name: "writer", Harness: profile.HarnessMock, Model: "model-2", Taste: 7, Diagnostics: 9, Intelligence: 8, Cost: 6, MaxConcurrent: 3, MaxCallsPerRun: 4, Description: "Writes\nclear code.", Uses: []string{profile.UseAgents}}
	disabled := enabled
	disabled.Name = "disabled-writer"
	disabled.Disabled = true
	e := newTestEnv(t, enabled, disabled)
	for _, role := range []string{"", RoleRoot} {
		meta := Meta{ID: "ses_20261019-000001-aaaaaaaa", Name: "reader", Room: e.room.Name, RoomID: e.room.ID, Role: role, Depth: 1}
		text := e.m.sessionInstructions(meta, "owner", 3, "").Text()
		for _, want := range []string{"[DYNA PROFILES]", "writer", "taste: 7/10", "diagnostics: 9/10", "intelligence: 8/10", "cost-efficiency: 6/10", "max-concurrent: 3", "max-calls-per-run: 4", "uses: agents", "dyna profiles list", "diagnostics = finding what is wrong with a solution (reviews, debugging)"} {
			if !strings.Contains(text, want) {
				t.Errorf("role %q missing %q", role, want)
			}
		}
		if strings.Contains(text, "Writes clear code.") || strings.Contains(text, "model-2") {
			t.Fatal("profile notes or model appeared in instructions")
		}
		if strings.Contains(text, "disabled-writer") {
			t.Fatal("disabled profile appeared in instructions")
		}
	}
}
