package subagent

import (
	"dyna-agent/internal/profile"
	"strings"
	"testing"
)

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
