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
		{Name: "signedout", Taste: 1},
		{Name: "b", Taste: 5, Cost: 5, Intelligence: 9},
		{Name: "a", Taste: 5, Cost: 5, Intelligence: 9},
		{Name: "cheap", Taste: 6, Cost: 4, Intelligence: 8},
	}
	failures := map[string]time.Time{"failed": now.Add(time.Minute), "signedout (not ready)": now.Add(time.Hour)}
	for _, tc := range []struct{ pin, want, reason string }{
		{"", "a", "auto: best taste + cost"},
		{"cheap", "cheap", "pinned by the room"},
		{"disabled", "a", "pinned keeper profile disabled is unavailable: it is disabled"},
		{"failed", "a", "pinned keeper profile failed is unavailable: its last turn failed at the provider, so it is held out until 2026-10-17T12:01:00Z"},
		{"signedout", "a", "pinned keeper profile signedout is unavailable: its last turn failed in a way that another model of the profile cannot fix, so it is held out until 2026-10-17T13:00:00Z"},
		{"missing", "a", "pinned keeper profile missing is unavailable: no profile has that name"},
	} {
		t.Run(tc.pin, func(t *testing.T) {
			choice, err := ChooseProfile(profiles, tc.pin, "", failures, now, "keeper")
			if err != nil || choice.Name != tc.want || choice.Reason != tc.reason {
				t.Fatalf("got %q %q %v", choice.Name, choice.Reason, err)
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
	if _, err := ChooseProfile(nil, "", "", nil, now, "keeper"); err == nil || !strings.Contains(err.Error(), "no enabled keeper profile") {
		t.Fatalf("unavailable: %v", err)
	}
}

func TestReviewerRanksByDiagnostics(t *testing.T) {
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	profiles := []profile.Profile{
		{Name: "stylish", Taste: 10, Cost: 4, Diagnostics: 3, Intelligence: 5},
		{Name: "sharp", Taste: 4, Cost: 4, Diagnostics: 10, Intelligence: 5},
	}
	if choice, err := ChooseProfile(profiles, "", "", nil, now, subagent.RoleReviewer); err != nil || choice.Name != "sharp" || choice.Reason != "auto: best diagnostics + cost" {
		t.Fatalf("reviewer chose %q %q, %v; want the high-diagnostics profile", choice.Name, choice.Reason, err)
	}
	// Other roles still choose a solution by taste.
	if choice, err := ChooseProfile(profiles, "", "", nil, now, subagent.RoleLibrarian); err != nil || choice.Name != "stylish" || choice.Reason != "auto: best taste + cost" {
		t.Fatalf("librarian chose %q %q, %v; want the high-taste profile", choice.Name, choice.Reason, err)
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

// A scan reads a session's turn only when its turn count changed or the cached
// turn had not ended. A steady session costs no read, and a running turn is read
// again until it ends.
func TestFailureCacheReadsOnlyChangedTurns(t *testing.T) {
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	var cache FailureCache
	infos := []subagent.Info{
		{Meta: subagent.Meta{ID: "quota", Profile: "argon", Turns: 1}},
		{Meta: subagent.Meta{ID: "healthy", Profile: "sol", Turns: 1}},
	}
	quota := "worker argon: protocol result ERROR: Individual quota reached. Resets in 152h54m19s."
	turns := map[string]subagent.Turn{
		"quota":   {Profile: "argon", N: 1, Created: now, Ended: now, Status: subagent.TurnError, FailureReason: providerfailure.Quota, Error: quota},
		"healthy": {Profile: "sol", N: 1, Created: now, Ended: now, Status: subagent.TurnOK},
	}
	var reads []string
	snapshot := func() ([]subagent.Info, error) { return infos, nil }
	read := func(id string, _ int) (subagent.Turn, error) {
		reads = append(reads, id)
		return turns[id], nil
	}
	got, err := cache.Recent(now, snapshot, read)
	if err != nil || got["argon"].IsZero() || len(reads) != 2 {
		t.Fatalf("first scan: %v %v reads %v", got, err, reads)
	}
	// Both turns ended and no turn count changed, so a later scan reads nothing.
	reads = nil
	got, err = cache.Recent(now.Add(2*time.Minute), snapshot, read)
	if err != nil || got["argon"].IsZero() || len(reads) != 0 {
		t.Fatalf("steady scan: %v %v reads %v", got, err, reads)
	}
	// The healthy session starts a turn that is still running, so only it is read.
	infos[1].Turns = 2
	turns["healthy"] = subagent.Turn{Profile: "sol", N: 2, Created: now.Add(3 * time.Minute), Status: subagent.TurnRunning}
	reads = nil
	if _, err := cache.Recent(now.Add(4*time.Minute), snapshot, read); err != nil || len(reads) != 1 || reads[0] != "healthy" {
		t.Fatalf("running scan read %v, want only healthy: %v", reads, err)
	}
	// That turn ends, and the next scan reads exactly that session again.
	turns["healthy"] = subagent.Turn{Profile: "sol", N: 2, Created: now.Add(3 * time.Minute), Ended: now.Add(5 * time.Minute), Status: subagent.TurnOK}
	reads = nil
	if _, err := cache.Recent(now.Add(6*time.Minute), snapshot, read); err != nil || len(reads) != 1 || reads[0] != "healthy" {
		t.Fatalf("ended scan read %v, want only healthy: %v", reads, err)
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
	if choice, err := ChooseProfile(profiles, "chain", "", RecordFailure(nil, info, quota, now), now, "reviewer"); err != nil || choice.Name != "chain" {
		t.Fatalf("a quota error hid a profile with healthy models: %q %v", choice.Name, err)
	}
	// A rejected sign-in blocks the whole account, so no model of the profile may
	// be chosen, even though the turn recorded no model cooldown.
	rejected := subagent.Turn{Status: subagent.TurnError, Ended: now, FailureReason: providerfailure.Auth, Error: "worker chain: authentication_error: invalid api key"}
	if choice, err := ChooseProfile(profiles, "chain", "", RecordFailure(nil, info, rejected, now), now, "reviewer"); err != nil || choice.Name != "spare" {
		t.Fatalf("a rejected sign-in kept the profile selectable: %q %v", choice.Name, err)
	}
	// A capacity failure that ended the turn already spent the whole model chain,
	// so the profile steps aside for the spare even though this engine process
	// holds no model cooldown for it.
	busy := subagent.Turn{Status: subagent.TurnError, Ended: now, FailureReason: providerfailure.Capacity, Error: "worker chain: Selected model is at capacity"}
	if got := RankProfiles(profiles, RecordFailure(nil, info, busy, now), now, "reviewer"); len(got) != 1 || got[0].Name != "spare" {
		t.Fatalf("a completed capacity failure kept the multi-model profile: %+v", got)
	}
	if choice, err := ChooseProfile(profiles, "chain", "", RecordFailure(nil, info, busy, now), now, "reviewer"); err != nil || choice.Name != "spare" {
		t.Fatalf("a completed capacity failure kept the profile selectable: %q %v", choice.Name, err)
	}
	signedOut := subagent.Turn{Status: subagent.TurnError, Ended: now, Error: "worker chain runtime is not ready: the active private account is not signed in"}
	failures := RecordFailure(nil, info, signedOut, now)
	if choice, err := ChooseProfile(profiles, "chain", "", failures, now, "reviewer"); err != nil || choice.Name != "spare" {
		t.Fatalf("not-ready profile chosen: %q %v", choice.Name, err)
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

// Provider quota reset times can be days away. A one-hour ban let the pool
// choose the exhausted profile again, and its reviews never started.
func TestQuotaResetKeepsProfileOut(t *testing.T) {
	storetest.Isolate(t)
	now := time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)
	info := subagent.Info{Meta: subagent.Meta{Profile: "argon"}}
	turn := subagent.Turn{Status: subagent.TurnError, Ended: now, FailureReason: providerfailure.Quota, Error: "worker argon: protocol result ERROR: Individual quota reached. Please upgrade your subscription to increase your limits. Resets in 152h54m19s."}
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

// D-110: an account whose saved quota limit outlasts the turn's own failure
// backoff keeps its profile out of automatic ranking, and a room that pinned
// it sees the typed cause while the hold lasts.
func TestQuotaHoldKeepsProfileOut(t *testing.T) {
	storetest.Isolate(t)
	now := time.Date(2026, 10, 25, 19, 45, 0, 0, time.UTC)
	kimi := profile.Profile{Name: "kimi-max", Taste: 6, Diagnostics: 8, Cost: 10}
	sol := profile.Profile{Name: "sol", Taste: 7, Diagnostics: 10, Cost: 8}
	failures := map[string]time.Time{quotaKey("kimi-max"): now.Add(time.Hour)}
	if got := RankProfiles([]profile.Profile{kimi, sol}, failures, now, subagent.RoleReviewer); len(got) != 1 || got[0].Name != "sol" {
		t.Fatalf("quota-blocked profile stayed ranked: %+v", got)
	}
	choice, err := ChooseProfile([]profile.Profile{kimi, sol}, "kimi-max", "", failures, now, subagent.RoleReviewer)
	if err != nil || choice.Name != "sol" || choice.Cause != CauseQuotaBlocked || !strings.Contains(choice.Reason, "out of quota") {
		t.Fatalf("pinned quota-blocked profile: %+v %v", choice, err)
	}
	if got := RankProfiles([]profile.Profile{kimi}, failures, now.Add(time.Hour), ""); len(got) != 1 {
		t.Fatal("quota hold did not expire at its reset")
	}
}

func TestConcurrentAccountRemainsSelectable(t *testing.T) {
	storetest.Isolate(t)
	lease, err := runtime.NewAccounts().TurnLeaseManaged(context.Background(), "pi")
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	choice, err := ChooseProfile([]profile.Profile{{Name: "parallel", Harness: "pi", Taste: 10}, {Name: "spare", Taste: 1}}, "", "", nil, time.Now(), "reviewer")
	if err != nil || choice.Name != "parallel" {
		t.Fatalf("concurrent account was excluded: profile=%q error=%v", choice.Name, err)
	}
}
