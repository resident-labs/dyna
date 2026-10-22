package runtime

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"dyna-agent/internal/profile"
)

func TestDoctorsShareChecksAndBoundSlowOnes(t *testing.T) {
	profiles := []profile.Profile{
		{Name: "opus", Harness: "claude-code"},
		{Name: "sonnet", Harness: "claude-code"},
		{Name: "luna", Harness: "codex"},
		{Name: "own", Harness: profile.HarnessCustom},
	}
	var calls atomic.Int32
	release := make(chan struct{})
	defer close(release)
	check := func(ctx context.Context, p profile.Profile) Doctor {
		calls.Add(1)
		if p.Harness == "codex" {
			<-release // a blocked check
		}
		return Doctor{Status: "ready"}
	}
	start := time.Now()
	got := doctors(context.Background(), profiles, 50*time.Millisecond, check, func(profile.Profile) Doctor { return Doctor{} })
	if time.Since(start) > 2*time.Second {
		t.Fatal("a blocked check held the status pass")
	}
	if calls.Load() != 3 {
		t.Fatalf("checks = %d, want one per harness and version", calls.Load())
	}
	want := map[string]string{"opus": "ready", "sonnet": "ready", "luna": "unavailable", "own": "ready"}
	for _, d := range got {
		if d.Status != want[d.Profile] || d.Harness == "" {
			t.Fatalf("doctor %+v, want status %s", d, want[d.Profile])
		}
	}
	if len(got) != len(profiles) || got[2].Profile != "luna" {
		t.Fatalf("doctors lost profile order: %+v", got)
	}
}

// A blocked check keeps the switch state that was read without it, so the page
// keeps polling a pending switch.
func TestDoctorsKeepSwitchStateWhenCheckIsBlocked(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	check := func(context.Context, profile.Profile) Doctor { <-release; return Doctor{Status: "ready"} }
	fallback := func(profile.Profile) Doctor { return Doctor{SwitchPending: true, RunningTurns: 2} }
	got := doctors(context.Background(), []profile.Profile{{Name: "opus", Harness: "claude-code"}}, 50*time.Millisecond, check, fallback)
	if d := got[0]; d.Status != "unavailable" || !d.SwitchPending || d.RunningTurns != 2 || d.Reason == "" {
		t.Fatalf("blocked check lost the switch state: %+v", d)
	}
}
