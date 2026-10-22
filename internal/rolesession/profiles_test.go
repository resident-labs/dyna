package rolesession

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/harness"
	runtime "dyna-agent/internal/harness/runtime"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/subagent"
)

func TestProfileSelection(t *testing.T) {
	storetest.Isolate(t)
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	profiles := []profile.Profile{
		{Name: "disabled", Taste: 100, Disabled: true},
		{Name: "failed", Taste: 90},
		{Name: "b", Taste: 5, Cost: 5, Intelligence: 9},
		{Name: "a", Taste: 5, Cost: 5, Intelligence: 9},
		{Name: "cheap", Taste: 6, Cost: 4, Intelligence: 8},
	}
	failures := map[string]time.Time{"failed": now.Add(time.Minute)}
	for _, tc := range []struct{ pin, want, reason string }{
		{"", "a", "auto: best taste + cost"},
		{"cheap", "cheap", "pinned by the room"},
		{"disabled", "a", "pinned profile unavailable; using best available profile"},
		{"failed", "a", "pinned profile unavailable; using best available profile"},
		{"missing", "a", "pinned profile unavailable; using best available profile"},
	} {
		t.Run(tc.pin, func(t *testing.T) {
			got, reason, err := ChooseProfile(profiles, tc.pin, failures, now, "keeper")
			if err != nil || got != tc.want || reason != tc.reason {
				t.Fatalf("got %q %q %v", got, reason, err)
			}
		})
	}
	got := RankProfiles(profiles, failures, now.Add(time.Minute))
	if got[0].Name != "failed" {
		t.Fatal("unavailable time did not expire")
	}
	if profiles[0].Name != "disabled" {
		t.Fatal("ranking mutated source")
	}
	if _, _, err := ChooseProfile(nil, "", nil, now, "keeper"); err == nil || !strings.Contains(err.Error(), "no enabled keeper profile") {
		t.Fatalf("unavailable: %v", err)
	}
}

func TestRecordFailure(t *testing.T) {
	now := time.Now()
	info := subagent.Info{Meta: subagent.Meta{Profile: "a"}}
	original := map[string]time.Time{"a": now.Add(-time.Minute)}
	turn := subagent.Turn{Status: subagent.TurnError, Error: "HTTP 429 quota exhausted"}
	got := RecordFailure(original, info, turn, now)
	if !got["a"].Equal(now.Add(time.Hour)) || !original["a"].Equal(now.Add(-time.Minute)) {
		t.Fatal("failure time or ownership changed")
	}
	turn.Ended = now.Add(-2 * time.Minute)
	if !RecordFailure(got, info, turn, now)["a"].Equal(now.Add(time.Hour)) {
		t.Fatal("older failure replaced newer failure")
	}
	for _, message := range []string{"provider.go could not parse input", "authentication helper test failed", "unauthorized local file", `failed: {"fallback_credit":null}`} {
		turn.Error = message
		if len(RecordFailure(nil, info, turn, now)) != 0 {
			t.Fatalf("unrelated failure: %s", message)
		}
	}
	for _, message := range []string{"HTTP 429", "quota exhausted", "insufficient credits", "usage limit reached", "service overloaded", "RATE_LIMIT",
		"authentication_error: invalid x-api-key", "503 Service Unavailable", "worker sol runtime is not ready: the active private account is not signed in"} {
		if !ProviderFailure(message) {
			t.Fatalf("missed %s", message)
		}
	}
}

func TestFailureCacheRecovery(t *testing.T) {
	now := time.Now()
	var cache FailureCache
	scans := 0
	infos := []subagent.Info{{Meta: subagent.Meta{ID: "failed", Profile: "a", Turns: 1}}}
	turns := map[string]subagent.Turn{"failed": {Profile: "a", Created: now.Add(-time.Minute), Ended: now.Add(-time.Minute), Status: subagent.TurnError, Error: "quota"}}
	snapshot := func() ([]subagent.Info, error) { scans++; return infos, nil }
	read := func(id string, n int) (subagent.Turn, error) {
		if n != 1 {
			t.Fatal("wrong turn")
		}
		return turns[id], nil
	}
	got, err := cache.Recent(now, snapshot, read)
	if err != nil || got["a"].IsZero() {
		t.Fatalf("missing failure: %v %v", got, err)
	}
	// Callers must not be able to alter the shared cache.
	delete(got, "a")
	infos = append(infos, subagent.Info{Meta: subagent.Meta{ID: "healthy", Profile: "a", Turns: 1}})
	turns["healthy"] = subagent.Turn{Created: now, Status: subagent.TurnOK}
	got, err = cache.Recent(now.Add(59*time.Second), snapshot, read)
	if err != nil || got["a"].IsZero() || scans != 1 {
		t.Fatal("minute cache not preserved")
	}
	got, err = cache.Recent(now.Add(time.Minute), snapshot, read)
	if err != nil || len(got) != 0 || scans != 2 {
		t.Fatal("healthy latest turn did not restore profile")
	}
	// A clock rollback must refresh instead of retaining a future cache.
	_, _ = cache.Recent(now, snapshot, read)
	if scans != 3 {
		t.Fatal("clock rollback did not refresh")
	}
}

func TestFailureCacheErrorsAndExpiry(t *testing.T) {
	now := time.Now()
	var cache FailureCache
	failure := errors.New("snapshot failed")
	if _, err := cache.Recent(now, func() ([]subagent.Info, error) { return nil, failure }, nil); !errors.Is(err, failure) {
		t.Fatalf("snapshot error: %v", err)
	}
	infos := []subagent.Info{{Meta: subagent.Meta{ID: "expired", Profile: "a"}}, {Meta: subagent.Meta{ID: "unreadable", Profile: "b"}}}
	got, err := cache.Recent(now, func() ([]subagent.Info, error) { return infos, nil }, func(id string, _ int) (subagent.Turn, error) {
		if id == "unreadable" {
			return subagent.Turn{}, errors.New("missing turn")
		}
		return subagent.Turn{Created: now.Add(-time.Hour), Ended: now.Add(-time.Hour), Status: subagent.TurnError, Error: "429"}, nil
	})
	if err != nil || len(got) != 0 {
		t.Fatalf("expired or unreadable failure: %v %v", got, err)
	}
}

func TestMultiModelProfileUsesModelCooldowns(t *testing.T) {
	storetest.Isolate(t)
	harness.ResetModelCooldownsForTest()
	t.Cleanup(harness.ResetModelCooldownsForTest)
	now := time.Now()
	p := profile.Profile{Name: "chain", Model: "a", Models: []string{"a", "b"}}
	failures := map[string]time.Time{p.Name: now}
	// Older failed turns cannot hide a profile with another healthy model.
	harness.RecordModelFailure(p.Name, "a", now)
	if got := RankProfiles([]profile.Profile{p}, failures, now); len(got) != 1 {
		t.Fatal("healthy fallback was excluded")
	}
	harness.RecordModelFailure(p.Name, "b", now)
	if got := RankProfiles([]profile.Profile{p}, failures, now); len(got) != 0 {
		t.Fatal("exhausted profile remained available")
	}
	if got := RankProfiles([]profile.Profile{p}, failures, now.Add(time.Hour)); len(got) != 1 {
		t.Fatal("cooldown did not expire")
	}
	harness.ClearModelFailure(p.Name, "b")
	if got := RankProfiles([]profile.Profile{p}, failures, now); len(got) != 1 {
		t.Fatal("healthy model did not restore profile")
	}
}

// A signed-out runtime fails before any model runs, so no model cooldown is
// recorded. The whole profile must still step aside for a healthy one.
func TestNotReadyExcludesMultiModelProfile(t *testing.T) {
	storetest.Isolate(t)
	harness.ResetModelCooldownsForTest()
	t.Cleanup(harness.ResetModelCooldownsForTest)
	now := time.Now()
	profiles := []profile.Profile{
		{Name: "chain", Model: "a", Models: []string{"a", "b"}, Taste: 9},
		{Name: "spare", Taste: 1},
	}
	info := subagent.Info{Meta: subagent.Meta{Profile: "chain"}}
	quota := subagent.Turn{Status: subagent.TurnError, Ended: now, Error: "worker chain: 429 quota exhausted"}
	if got, _, err := ChooseProfile(profiles, "chain", RecordFailure(nil, info, quota, now), now, "reviewer"); err != nil || got != "chain" {
		t.Fatalf("a quota error hid a profile with healthy models: %q %v", got, err)
	}
	signedOut := subagent.Turn{Status: subagent.TurnError, Ended: now, Error: "worker chain runtime is not ready: the active private account is not signed in"}
	failures := RecordFailure(nil, info, signedOut, now)
	if got, _, err := ChooseProfile(profiles, "chain", failures, now, "reviewer"); err != nil || got != "spare" {
		t.Fatalf("not-ready profile chosen: %q %v", got, err)
	}
	var cache FailureCache
	cached, err := cache.Recent(now, func() ([]subagent.Info, error) { return []subagent.Info{info}, nil }, func(string, int) (subagent.Turn, error) { return signedOut, nil })
	if err != nil {
		t.Fatal(err)
	}
	if got := RankProfiles(profiles, cached, now); len(got) != 1 || got[0].Name != "spare" {
		t.Fatalf("cached not-ready failure ignored: %+v", got)
	}
	if got := RankProfiles(profiles, failures, now.Add(time.Hour)); len(got) != 2 {
		t.Fatal("not-ready failure did not expire")
	}
}

// Antigravity gave a reset time six days away. A one-hour ban let the pool
// choose the exhausted profile again, and its reviews never started.
func TestQuotaResetKeepsProfileOut(t *testing.T) {
	storetest.Isolate(t)
	now := time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)
	info := subagent.Info{Meta: subagent.Meta{Profile: "argon"}}
	turn := subagent.Turn{Status: subagent.TurnError, Ended: now, Error: "worker argon: Antigravity protocol result ERROR: Individual quota reached. Please upgrade your subscription to increase your limits. Resets in 152h54m19s."}
	failures := RecordFailure(nil, info, turn, now)
	if want := now.Add(152*time.Hour + 54*time.Minute + 19*time.Second); !failures["argon"].Equal(want) {
		t.Fatalf("until = %v, want %v", failures["argon"], want)
	}
	profiles := []profile.Profile{{Name: "argon"}}
	if got := RankProfiles(profiles, failures, now.Add(2*time.Hour)); len(got) != 0 {
		t.Fatal("exhausted profile returned before its reset")
	}
	if got := RankProfiles(profiles, failures, now.Add(153*time.Hour)); len(got) != 1 {
		t.Fatal("profile stayed out after its reset")
	}

	var cache FailureCache
	turns := map[string]subagent.Turn{
		"quota": {Profile: "argon", Created: now, Ended: now, Status: subagent.TurnError, Error: turn.Error},
		// A later canceled turn proves nothing about the quota.
		"stopped": {Profile: "argon", Created: now.Add(time.Minute), Ended: now.Add(2 * time.Minute), Status: subagent.TurnStopped, Error: "worker argon canceled/timed out: context canceled"},
	}
	infos := []subagent.Info{{Meta: subagent.Meta{ID: "quota", Profile: "argon"}}, {Meta: subagent.Meta{ID: "stopped", Profile: "argon"}}}
	got, err := cache.Recent(now.Add(2*time.Hour), func() ([]subagent.Info, error) { return infos, nil }, func(id string, _ int) (subagent.Turn, error) { return turns[id], nil })
	if err != nil || !got["argon"].After(now.Add(152*time.Hour)) {
		t.Fatalf("cache lost the quota failure: %v %v", got, err)
	}
}

func TestBusySerialAccountFallsBackAndRecovers(t *testing.T) {
	storetest.Isolate(t)
	profiles := []profile.Profile{
		{Name: "serial", Harness: "antigravity", Taste: 10},
		{Name: "disabled", Harness: "codex", Taste: 100, Disabled: true},
		{Name: "spare", Harness: "codex", Taste: 1},
	}
	now := time.Now()
	lease, err := runtime.NewAccounts().TurnLeaseManaged(context.Background(), "antigravity")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Close() })
	for _, pin := range []string{"", "serial"} {
		got, _, err := ChooseProfile(profiles, pin, nil, now, "reviewer")
		if err != nil || got != "spare" {
			t.Fatalf("busy serial account selected: pin=%q profile=%q error=%v", pin, got, err)
		}
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	got, _, err := ChooseProfile(profiles, "", nil, now, "reviewer")
	if err != nil || got != "serial" {
		t.Fatalf("released account not selectable: profile=%q error=%v", got, err)
	}
}

func TestConcurrentAccountRemainsSelectable(t *testing.T) {
	storetest.Isolate(t)
	lease, err := runtime.NewAccounts().TurnLeaseManaged(context.Background(), "codex")
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	got, _, err := ChooseProfile([]profile.Profile{{Name: "parallel", Harness: "codex", Taste: 10}, {Name: "spare", Taste: 1}}, "", nil, time.Now(), "reviewer")
	if err != nil || got != "parallel" {
		t.Fatalf("concurrent account was excluded: profile=%q error=%v", got, err)
	}
}
