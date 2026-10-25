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
	"dyna-agent/internal/providerfailure"
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
	got := RankProfiles(profiles, failures, now.Add(time.Minute), "")
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

func TestReviewerRanksByDiagnostics(t *testing.T) {
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	profiles := []profile.Profile{
		{Name: "stylish", Taste: 10, Cost: 4, Diagnostics: 3, Intelligence: 5},
		{Name: "sharp", Taste: 4, Cost: 4, Diagnostics: 10, Intelligence: 5},
	}
	if got, reason, err := ChooseProfile(profiles, "", nil, now, subagent.RoleReviewer); err != nil || got != "sharp" || reason != "auto: best diagnostics + cost" {
		t.Fatalf("reviewer chose %q %q, %v; want the high-diagnostics profile", got, reason, err)
	}
	// Other roles still choose a solution by taste.
	if got, reason, err := ChooseProfile(profiles, "", nil, now, subagent.RoleLibrarian); err != nil || got != "stylish" || reason != "auto: best taste + cost" {
		t.Fatalf("librarian chose %q %q, %v; want the high-taste profile", got, reason, err)
	}
	// A profile saved before the rating existed ranks as 5, the neutral value,
	// so it beats an explicit 4 even though its stored value is 0.
	old := []profile.Profile{{Name: "old", Cost: 4}, {Name: "low", Cost: 4, Diagnostics: 4}}
	if got := RankProfiles(old, nil, now, subagent.RoleReviewer); got[0].Name != "old" {
		t.Fatalf("unset diagnostics ranked %q first, want old as neutral 5", got[0].Name)
	}
}

func TestRecordFailure(t *testing.T) {
	now := time.Now()
	info := subagent.Info{Meta: subagent.Meta{Profile: "a"}}
	original := map[string]time.Time{"a": now.Add(-time.Minute)}
	turn := subagent.Turn{Status: subagent.TurnError, Error: "HTTP 429 quota exhausted", FailureReason: providerfailure.Quota}
	got := RecordFailure(original, info, turn, now)
	if !got["a"].Equal(now.Add(time.Hour)) || !original["a"].Equal(now.Add(-time.Minute)) {
		t.Fatal("failure time or ownership changed")
	}
	turn.Ended = now.Add(-2 * time.Minute)
	if !RecordFailure(got, info, turn, now)["a"].Equal(now.Add(time.Hour)) {
		t.Fatal("older failure replaced newer failure")
	}
	// Display text alone never blocks a profile, whatever it happens to say.
	for _, message := range []string{"HTTP 429 quota exhausted", "provider.go could not parse input", "authentication helper test failed", "unauthorized local file", `failed: {"fallback_credit":null}`} {
		turn := subagent.Turn{Status: subagent.TurnError, Error: message}
		if len(RecordFailure(nil, info, turn, now)) != 0 {
			t.Fatalf("untyped failure blocked the profile: %s", message)
		}
		if ProviderFailure(turn) {
			t.Fatalf("untyped failure counted as a provider failure: %s", message)
		}
	}
	// Each named reason a turn records is a provider failure the profile must
	// not retry on its own, except capacity, which is only a busy provider.
	for _, reason := range []providerfailure.Reason{providerfailure.Quota, providerfailure.Auth, providerfailure.Unavailable} {
		turn := subagent.Turn{Status: subagent.TurnError, Error: "provider failure", FailureReason: reason}
		if !ProviderFailure(turn) || len(RecordFailure(nil, info, turn, now)) != 1 {
			t.Fatalf("reason %q did not block the profile", reason)
		}
	}
	// A busy provider also blocks after the turn ended: its backoff and model
	// chain are spent, so the role must move to a healthy profile.
	busy := subagent.Turn{Status: subagent.TurnError, Error: "service overloaded", FailureReason: providerfailure.Capacity}
	if !ProviderFailure(busy) || len(RecordFailure(nil, info, busy, now)) != 1 {
		t.Fatal("an exhausted capacity failure did not block the profile")
	}
	// A runtime that failed before the model ran keeps its own key.
	notReady := subagent.Turn{Status: subagent.TurnError, Error: "worker sol runtime is not ready: the active private account is not signed in"}
	if !ProviderFailure(notReady) || RecordFailure(nil, info, notReady, now)[notReadyKey("a")].IsZero() {
		t.Fatal("a runtime that is not ready did not block its profile")
	}
}

func TestFailureCacheRecovery(t *testing.T) {
	now := time.Now()
	var cache FailureCache
	scans := 0
	infos := []subagent.Info{{Meta: subagent.Meta{ID: "failed", Profile: "a", Turns: 1}}}
	turns := map[string]subagent.Turn{"failed": {Profile: "a", Created: now.Add(-time.Minute), Ended: now.Add(-time.Minute), Status: subagent.TurnError, Error: "quota", FailureReason: providerfailure.Quota}}
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
		return subagent.Turn{Created: now.Add(-time.Hour), Ended: now.Add(-time.Hour), Status: subagent.TurnError, Error: "429", FailureReason: providerfailure.Quota}, nil
	})
	if err != nil || len(got) != 0 {
		t.Fatalf("expired or unreadable failure: %v %v", got, err)
	}
}

func TestMultiModelProfileUsesModelCooldowns(t *testing.T) {
	storetest.Isolate(t)
	now := time.Now()
	p := profile.Profile{Name: "chain", Model: "a", Models: []string{"a", "b"}}
	failures := map[string]time.Time{p.Name: now}
	// Older failed turns cannot hide a profile with another healthy model.
	harness.RecordModelFailure(p.Name, "a", now)
	if got := RankProfiles([]profile.Profile{p}, failures, now, ""); len(got) != 1 {
		t.Fatal("healthy fallback was excluded")
	}
	harness.RecordModelFailure(p.Name, "b", now)
	if got := RankProfiles([]profile.Profile{p}, failures, now, ""); len(got) != 0 {
		t.Fatal("exhausted profile remained available")
	}
	if got := RankProfiles([]profile.Profile{p}, failures, now.Add(time.Hour), ""); len(got) != 1 {
		t.Fatal("cooldown did not expire")
	}
	harness.ClearModelFailure(p.Name, "b")
	if got := RankProfiles([]profile.Profile{p}, failures, now, ""); len(got) != 1 {
		t.Fatal("healthy model did not restore profile")
	}
}

// A signed-out runtime fails before any model runs, so no model cooldown is
// recorded. The whole profile must still step aside for a healthy one. The same
// holds for a completed capacity failure and a rejected sign-in.
func TestNotReadyExcludesMultiModelProfile(t *testing.T) {
	storetest.Isolate(t)
	now := time.Now()
	profiles := []profile.Profile{
		{Name: "chain", Model: "a", Models: []string{"a", "b"}, Taste: 9},
		{Name: "spare", Taste: 1},
	}
	info := subagent.Info{Meta: subagent.Meta{Profile: "chain"}}
	quota := subagent.Turn{Status: subagent.TurnError, Ended: now, FailureReason: providerfailure.Quota, Error: "worker chain: 429 quota exhausted"}
	if got, _, err := ChooseProfile(profiles, "chain", RecordFailure(nil, info, quota, now), now, "reviewer"); err != nil || got != "chain" {
		t.Fatalf("a quota error hid a profile with healthy models: %q %v", got, err)
	}
	// A rejected sign-in blocks the whole account, so no model of the profile may
	// be chosen, even though the turn recorded no model cooldown.
	rejected := subagent.Turn{Status: subagent.TurnError, Ended: now, FailureReason: providerfailure.Auth, Error: "worker chain: authentication_error: invalid api key"}
	if got, _, err := ChooseProfile(profiles, "chain", RecordFailure(nil, info, rejected, now), now, "reviewer"); err != nil || got != "spare" {
		t.Fatalf("a rejected sign-in kept the profile selectable: %q %v", got, err)
	}
	// A capacity failure that ended the turn already spent the whole model chain,
	// so the profile steps aside for the spare even though this engine process
	// holds no model cooldown for it.
	busy := subagent.Turn{Status: subagent.TurnError, Ended: now, FailureReason: providerfailure.Capacity, Error: "worker chain: Selected model is at capacity"}
	if got := RankProfiles(profiles, RecordFailure(nil, info, busy, now), now, "reviewer"); len(got) != 1 || got[0].Name != "spare" {
		t.Fatalf("a completed capacity failure kept the multi-model profile: %+v", got)
	}
	if got, _, err := ChooseProfile(profiles, "chain", RecordFailure(nil, info, busy, now), now, "reviewer"); err != nil || got != "spare" {
		t.Fatalf("a completed capacity failure kept the profile selectable: %q %v", got, err)
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
	if got := RankProfiles(profiles, cached, now, ""); len(got) != 1 || got[0].Name != "spare" {
		t.Fatalf("cached not-ready failure ignored: %+v", got)
	}
	if got := RankProfiles(profiles, failures, now.Add(time.Hour), ""); len(got) != 2 {
		t.Fatal("not-ready failure did not expire")
	}
}

// Antigravity gave a reset time six days away. A one-hour ban let the pool
// choose the exhausted profile again, and its reviews never started.
func TestQuotaResetKeepsProfileOut(t *testing.T) {
	storetest.Isolate(t)
	now := time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)
	info := subagent.Info{Meta: subagent.Meta{Profile: "argon"}}
	turn := subagent.Turn{Status: subagent.TurnError, Ended: now, FailureReason: providerfailure.Quota, Error: "worker argon: Antigravity protocol result ERROR: Individual quota reached. Please upgrade your subscription to increase your limits. Resets in 152h54m19s."}
	failures := RecordFailure(nil, info, turn, now)
	if want := now.Add(152*time.Hour + 54*time.Minute + 19*time.Second); !failures["argon"].Equal(want) {
		t.Fatalf("until = %v, want %v", failures["argon"], want)
	}
	profiles := []profile.Profile{{Name: "argon"}}
	if got := RankProfiles(profiles, failures, now.Add(2*time.Hour), ""); len(got) != 0 {
		t.Fatal("exhausted profile returned before its reset")
	}
	if got := RankProfiles(profiles, failures, now.Add(153*time.Hour), ""); len(got) != 1 {
		t.Fatal("profile stayed out after its reset")
	}

	var cache FailureCache
	turns := map[string]subagent.Turn{
		"quota": {Profile: "argon", Created: now, Ended: now, Status: subagent.TurnError, FailureReason: providerfailure.Quota, Error: turn.Error},
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
