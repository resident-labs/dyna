// Package rolesession shares lifecycle decisions for named room role sessions.
// Callers own role state and hold an action lock while applying decisions.
package rolesession

import (
	"fmt"
	"slices"
	"sort"
	"sync"
	"time"

	"dyna-agent/internal/harness"
	runtime "dyna-agent/internal/harness/runtime"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/subagent"
)

// ProviderFailure reports a turn error that makes its profile unavailable: a
// provider error, or a runtime that failed before the model ran.
func ProviderFailure(text string) bool {
	return harness.IsProviderFailure(text) || harness.IsNotReady(text)
}

// failureKey names the failure entry for a turn. A runtime that is not ready
// fails before any model runs, so it blocks the whole profile under its own
// key. Provider errors stay under the profile name, which multi-model profiles
// replace with their model cooldowns.
func failureKey(profileName, text string) string {
	if harness.IsNotReady(text) {
		return notReadyKey(profileName)
	}
	return profileName
}

func notReadyKey(profileName string) string { return profileName + " (not ready)" }

// RankProfiles excludes disabled profiles, profiles whose runtime is not ready,
// and profiles that are unavailable until a later time. Multi-model profiles
// remain available while any model is healthy. role orders the result; see
// rankBefore.
func RankProfiles(profiles []profile.Profile, failures map[string]time.Time, now time.Time, role string) []profile.Profile {
	out := []profile.Profile{}
	accounts := runtime.NewAccounts()
	busy := map[string]bool{}
	for _, p := range profiles {
		if p.Disabled || now.Before(failures[notReadyKey(p.Name)]) {
			continue
		}
		if adapter, err := runtime.Adapter(p.Harness); err == nil && adapter.Exclusive {
			unavailable, known := busy[adapter.Harness]
			if !known {
				pending, running := accounts.SwitchStatus(adapter.Harness)
				unavailable = pending || running > 0
				busy[adapter.Harness] = unavailable
			}
			if unavailable {
				continue
			}
		}
		models := p.EffectiveModels()
		if len(models) > 1 {
			for _, model := range models {
				if !harness.IsModelCoolingDown(p.Name, model, now) {
					out = append(out, p)
					break
				}
			}
		} else if !now.Before(failures[p.Name]) {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return rankBefore(role, out[i], out[j]) })
	return out
}

// rankBefore reports whether a outranks b for role. A reviewer checks finished
// work, so it ranks by diagnostics plus cost. Other roles choose solutions, so
// they rank by taste plus cost. Both then take higher intelligence, then name.
func rankBefore(role string, a, b profile.Profile) bool {
	score := func(p profile.Profile) int { return p.Taste + p.Cost }
	if role == subagent.RoleReviewer {
		score = func(p profile.Profile) int { return p.EffectiveDiagnostics() + p.Cost }
	}
	if sa, sb := score(a), score(b); sa != sb {
		return sa > sb
	}
	if a.Intelligence != b.Intelligence {
		return a.Intelligence > b.Intelligence
	}
	return a.Name < b.Name
}

// ChooseProfile honors an available pin, then uses the shared ranking for role.
// role orders the result and names the profile kind in the error message.
func ChooseProfile(profiles []profile.Profile, pinned string, failures map[string]time.Time, now time.Time, role string) (string, string, error) {
	// Services never run subagents-only profiles.
	profiles = slices.DeleteFunc(slices.Clone(profiles), func(p profile.Profile) bool { return !p.ForServices() })
	ranked := RankProfiles(profiles, failures, now, role)
	if pinned != "" {
		for _, p := range ranked {
			if p.Name == pinned {
				return p.Name, "pinned by the room", nil
			}
		}
	}
	if len(ranked) == 0 {
		return "", "", fmt.Errorf("no enabled %s profile is available; all profiles are disabled or recently failed", role)
	}
	reason := "auto: best taste + cost"
	if role == subagent.RoleReviewer {
		reason = "auto: best diagnostics + cost"
	}
	if pinned != "" {
		reason = "pinned profile unavailable; using best available profile"
	}
	return ranked[0].Name, reason, nil
}

// MergeFailures returns an independent map containing the latest unavailable
// times.
func MergeFailures(sets ...map[string]time.Time) map[string]time.Time {
	out := map[string]time.Time{}
	for _, failures := range sets {
		for name, at := range failures {
			if at.After(out[name]) {
				out[name] = at
			}
		}
	}
	return out
}

// RecordFailure returns a copy and records provider failures only, as the time
// the profile becomes available again. A missing end time uses now, and an
// earlier time cannot replace a later one.
func RecordFailure(failures map[string]time.Time, info subagent.Info, turn subagent.Turn, now time.Time) map[string]time.Time {
	out := MergeFailures(failures)
	if turn.Status == subagent.TurnError && ProviderFailure(turn.Error) {
		at := turn.Ended
		if at.IsZero() {
			at = now
		}
		if key, until := failureKey(info.Profile, turn.Error), harness.UnavailableUntil(turn.Error, at); until.After(out[key]) {
			out[key] = until
		}
	}
	return out
}

// FailureCache scans at most once per minute. The latest successful or failed
// turn for each profile controls availability, so a newer successful turn
// clears an older failure. A stopped turn says nothing about the provider.
// Snapshot and read are callbacks so tests need no provider or session files.
type FailureCache struct {
	mu       sync.Mutex
	checked  time.Time
	failures map[string]time.Time
}

func (c *FailureCache) Recent(now time.Time, snapshot func() ([]subagent.Info, error), read func(string, int) (subagent.Turn, error)) (map[string]time.Time, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failures != nil && !now.Before(c.checked) && now.Before(c.checked.Add(time.Minute)) {
		return MergeFailures(c.failures), nil
	}
	infos, err := snapshot()
	if err != nil {
		return nil, err
	}
	latest := map[string]subagent.Turn{}
	for _, info := range infos {
		t, err := read(info.ID, info.Turns)
		failed := t.Status == subagent.TurnError && ProviderFailure(t.Error)
		if err != nil || (t.Status != subagent.TurnOK && !failed) {
			continue
		}
		name := t.Profile
		if name == "" {
			name = info.Profile
		}
		if prior, ok := latest[name]; !ok || t.Created.After(prior.Created) {
			latest[name] = t
		}
	}
	failures := map[string]time.Time{}
	for name, t := range latest {
		if t.Status == subagent.TurnError {
			if until := harness.UnavailableUntil(t.Error, t.Ended); now.Before(until) {
				failures[failureKey(name, t.Error)] = until
			}
		}
	}
	c.checked, c.failures = now, failures
	return MergeFailures(failures), nil
}

var failureCaches sync.Map

// RecentProfileFailures shares the provider cache across roles using one store.
func RecentProfileFailures(store *subagent.Store, now time.Time) (map[string]time.Time, error) {
	value, _ := failureCaches.LoadOrStore(store.Root, &FailureCache{})
	return value.(*FailureCache).Recent(now, func() ([]subagent.Info, error) { return store.ListSnapshot(true) }, store.ReadTurn)
}
