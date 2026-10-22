package subagent

import (
	"dyna-agent/internal/profile"
	"strings"
	"testing"
)

func TestProfilesAppearInRootAndWorkerInstructions(t *testing.T) {
	enabled := profile.Profile{Name: "writer", Harness: profile.HarnessMock, Model: "model-2", Taste: 7, Intelligence: 8, Cost: 6, MaxConcurrent: 3, Description: "Writes\nclear code.", Uses: []string{profile.UseAgents}}
	disabled := enabled
	disabled.Name = "disabled-writer"
	disabled.Disabled = true
	e := newTestEnv(t, enabled, disabled)
	for _, role := range []string{"", RoleRoot} {
		meta := Meta{ID: "ses_20261019-000001-aaaaaaaa", Name: "reader", Room: e.room.Name, RoomID: e.room.ID, Role: role, Depth: 1}
		text := e.m.sessionInstructions(meta, "owner", 3, "").Text()
		for _, want := range []string{"[DYNA PROFILES]", "writer", "model: model-2", "taste: 7/10", "intelligence: 8/10", "cost-efficiency: 6/10", "max-concurrent: 3", "Writes clear code."} {
			if !strings.Contains(text, want) {
				t.Errorf("role %q missing %q", role, want)
			}
		}
		if strings.Contains(text, "disabled-writer") {
			t.Fatal("disabled profile appeared in instructions")
		}
	}
}
