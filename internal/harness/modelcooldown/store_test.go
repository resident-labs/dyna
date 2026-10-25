package modelcooldown_test

import (
	"context"
	"testing"
	"time"

	"dyna-agent/internal/harness"
	"dyna-agent/internal/harness/modelcooldown"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/rolesession"
	"dyna-agent/internal/store/storetest"
)

// Turn runners are separate processes. A model that one turn saw fail must
// still be cooling down for the next turn, in another process, and a success
// must delete it. The test reads the durable rows back through its own store
// and checks the model chain and the role ranking that consume them.
func TestFailureSurvivesTheTurnThatRecordedIt(t *testing.T) {
	dataDir := storetest.DataDir(t)
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	p := profile.Profile{Name: "chain", Model: "a", Models: []string{"a", "b"}, Taste: 9}
	reader := modelcooldown.Store{DataDir: dataDir}

	harness.RecordModelFailure(p.Name, "a", now)
	failures, err := reader.Failures(context.Background(), p.Name)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := failures["a"]; !ok || !got.Equal(now) {
		t.Fatalf("a new reader saw failures %v, want a failed at %s", failures, now)
	}

	// The next turn skips the model the first turn saw fail.
	if first, candidates := harness.SelectHealthyModel(p, now); first != "b" || len(candidates) != 1 {
		t.Fatalf("next turn selected %q of %v, want b alone", first, candidates)
	}
	// One healthy model keeps the whole profile in the role ranking.
	if got := rolesession.RankProfiles([]profile.Profile{p}, nil, now, ""); len(got) != 1 {
		t.Fatalf("role ranking dropped a profile with a healthy model: %v", got)
	}

	// Once every model is cooling down, the profile leaves the role ranking.
	harness.RecordModelFailure(p.Name, "b", now)
	if got := rolesession.RankProfiles([]profile.Profile{p}, nil, now, ""); len(got) != 0 {
		t.Fatalf("role ranking kept an exhausted profile: %v", got)
	}
	// The cooldown ends one hour later, so the profile returns on its own.
	if got := rolesession.RankProfiles([]profile.Profile{p}, nil, now.Add(time.Hour), ""); len(got) != 1 {
		t.Fatalf("expired cooldowns did not restore the profile: %v", got)
	}

	// A turn that succeeds on b deletes b's cooldown for every reader.
	harness.ClearModelFailure(p.Name, "b")
	failures, err = reader.Failures(context.Background(), p.Name)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := failures["b"]; ok {
		t.Fatalf("success left b cooling down: %v", failures)
	}
	if got := rolesession.RankProfiles([]profile.Profile{p}, nil, now, ""); len(got) != 1 {
		t.Fatalf("role ranking ignored the deletion: %v", got)
	}

	// A single-model profile has no other model to fall back on, so its own
	// model's cooldown keeps it out even when no recent-turn failure is cached.
	one := profile.Profile{Name: "single", Model: "solo", Taste: 1}
	harness.RecordModelFailure(one.Name, "solo", now)
	if got := rolesession.RankProfiles([]profile.Profile{one}, nil, now, ""); len(got) != 0 {
		t.Fatalf("role ranking kept a cooling single-model profile: %v", got)
	}
	harness.ClearModelFailure(one.Name, "solo")
	if got := rolesession.RankProfiles([]profile.Profile{one}, nil, now, ""); len(got) != 1 {
		t.Fatalf("role ranking kept a single-model profile out after success: %v", got)
	}

	// A profile that names no model runs its harness default, so its failure is
	// recorded under the empty model key that effective models resolve to.
	empty := profile.Profile{Name: "default"}
	harness.RecordModelFailure(empty.Name, "", now)
	if got := rolesession.RankProfiles([]profile.Profile{empty}, nil, now, ""); len(got) != 0 {
		t.Fatalf("role ranking kept a cooling default-model profile: %v", got)
	}
}
