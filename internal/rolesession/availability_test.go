package rolesession

import (
	"testing"
	"time"

	runtime "dyna-agent/internal/harness/runtime"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/subagent"
)

func TestSessionAvailabilityRejectsIneligibleProfiles(t *testing.T) {
	profiles := []profile.Profile{
		{Name: "disabled", Disabled: true},
		{Name: "service", Uses: []string{profile.UseServices}},
	}
	got := SessionAvailability(profiles, "repo", &subagent.Store{Root: t.TempDir()}, time.Now())
	for _, candidate := range got {
		if candidate.State != runtime.QuotaUnavailable {
			t.Fatalf("%s: got %s, want unavailable", candidate.Profile, candidate.State)
		}
	}
}
