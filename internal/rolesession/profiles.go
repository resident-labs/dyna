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
	"dyna-agent/internal/providerfailure"
	"dyna-agent/internal/subagent"
)

// ProviderFailure reports a turn that the provider or the runtime caused, so a
// replacement session would fail the same way. Recovery routes on the reason
// the turn recorded; this never reads the display text for a provider error.
func ProviderFailure(turn subagent.Turn) bool {
	return turn.FailureReason != "" || harness.IsNotReady(turn.Error)
}

// unavailable reports whether a failed turn keeps its profile out until it
// recovers. A typed provider failure does, including capacity: that turn already
// spent its capacity backoff and model chain, so the profile must step aside
// rather than start the same busy provider again.
func unavailable(turn subagent.Turn) bool {
	return harness.IsNotReady(turn.Error) || turn.FailureReason.BlocksProfile()
}

// failureKey names the failure entry for a turn. A runtime that is not ready
// fails before any model runs, a rejected sign-in rejects the whole account,
// and a capacity failure that ended a turn has already spent every model of the
// chain. None can be fixed by picking another model of the same profile, so all
// three block the whole profile under the whole-profile key. A quota failure
// stays under the profile name, because another model of the profile may still
// work and its model cooldowns handle that.
func failureKey(profileName string, turn subagent.Turn) string {
	switch {
	case harness.IsNotReady(turn.Error), turn.FailureReason == providerfailure.Auth, turn.FailureReason == providerfailure.Capacity:
		return notReadyKey(profileName)
	}
	return profileName
}

// notReadyKey is the whole-profile key: RankProfiles excludes the profile while
// this key is in the future, whatever models it has. It names a failure of the
// profile itself, not of one model.
func notReadyKey(profileName string) string { return profileName + " (not ready)" }

// quotaKey is the account-quota key: RankProfiles excludes the profile while
// this key is in the future. An account failover recorded the limit after a
// real quota rejection, so the hold lasts until the account's own reset, not
// the shorter backoff a failed turn records.
func quotaKey(profileName string) string { return profileName + " (quota)" }

// RankProfiles excludes disabled profiles, profiles that carry a failure of the
// whole profile, and profiles that are unavailable until a later time. The
// failure map may carry the account-quota key of AccountQuotaHolds. Multi-model
// profiles remain available while any model is healthy, unless the
// whole-profile key excludes them. A single-model profile stays out while that
// model cools down. role orders the result; see rankBefore.
func RankProfiles(profiles []profile.Profile, failures map[string]time.Time, now time.Time, role string) []profile.Profile {
	out := []profile.Profile{}
	accounts := runtime.NewAccounts()
	busy := map[string]bool{}
	for _, p := range profiles {
		if cause, _ := blockReason(p, failures, now); cause != "" {
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
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return rankBefore(role, out[i], out[j]) })
	return out
}

// Cause names why a role cannot use a profile, independently of its display
// text. The zero value means the profile is available.
type Cause string

const (
	CauseMissing        Cause = "missing"
	CauseDisabled       Cause = "disabled"
	CauseNotForServices Cause = "not-for-services"
	CauseNotReady       Cause = "not-ready"
	CauseQuotaBlocked   Cause = "quota-blocked"
	CauseProviderFailed Cause = "provider-failed"
	CauseModelCooling   Cause = "model-cooling-down"
	CauseHarnessBusy    Cause = "harness-busy"
)

// blockReason names why the profile cannot run now, or returns no cause when it
// can. The detail is the exact cause, so a caller can report it to the owner.
func blockReason(p profile.Profile, failures map[string]time.Time, now time.Time) (Cause, string) {
	if p.Disabled {
		return CauseDisabled, "it is disabled"
	}
	if at := failures[notReadyKey(p.Name)]; now.Before(at) {
		// This key holds every failure that no other model of the profile can
		// fix: a runtime that did not start, a rejected sign-in, and exhausted
		// capacity. Health names the runtime cause when it has one.
		return CauseNotReady, fmt.Sprintf("its last turn failed in a way that another model of the profile cannot fix, so it is held out until %s", at.Format(time.RFC3339))
	}
	if at := failures[quotaKey(p.Name)]; now.Before(at) {
		// Every account that may run the profile is out of quota until the
		// account's own reset, so another model of the profile cannot help.
		return CauseQuotaBlocked, fmt.Sprintf("every account that may run it is out of quota until %s", at.Format(time.RFC3339))
	}
	models := p.EffectiveModels()
	if len(models) > 1 {
		// A multi-model profile stays available while any model is healthy.
		for _, model := range models {
			if !harness.IsModelCoolingDown(p.Name, model, now) {
				return "", ""
			}
		}
		return CauseModelCooling, "every model is cooling down after a provider failure"
	}
	// A single-model profile has no other model to fall back on, so both its
	// failure entry and its model's cooldown keep it out. The model key is the
	// one effective models give, else Model for a harness default.
	if at := failures[p.Name]; now.Before(at) {
		return CauseProviderFailed, fmt.Sprintf("its last turn failed at the provider, so it is held out until %s", at.Format(time.RFC3339))
	}
	model := p.Model
	if len(models) == 1 {
		model = models[0]
	}
	if harness.IsModelCoolingDown(p.Name, model, now) {
		return CauseModelCooling, fmt.Sprintf("model %s is cooling down after a provider failure", model)
	}
	return "", ""
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

// Choice is the profile a role runs, the reason for it, and the typed cause
// when the room pinned another profile that the role cannot use now.
type Choice struct {
	Name   string
	Reason string
	Cause  Cause
}

// ChooseProfile honors an available pin, then uses the shared ranking for role.
// role orders the result and names the profile kind in the error message. A
// profile whose every account that may run in repo is out of quota is
// unavailable until the account's own reset, whatever the turn records say.
func ChooseProfile(profiles []profile.Profile, pinned, repo string, failures map[string]time.Time, now time.Time, role string) (Choice, error) {
	// Account quota holds come from the account store, not the turn records,
	// so an exhausted account keeps its profile out until its own reset.
	failures = MergeFailures(failures, AccountQuotaHolds(profiles, repo, now))
	// Services never run subagents-only profiles. Keep the full list to explain
	// a pinned profile that this filter removed.
	usable := slices.DeleteFunc(slices.Clone(profiles), func(p profile.Profile) bool { return !p.ForServices() })
	ranked := RankProfiles(usable, failures, now, role)
	if pinned != "" {
		for _, p := range ranked {
			if p.Name == pinned {
				return Choice{Name: p.Name, Reason: "pinned by the room"}, nil
			}
		}
	}
	if len(ranked) == 0 {
		return Choice{}, fmt.Errorf("no enabled %s profile is available; all profiles are disabled or recently failed", role)
	}
	if pinned != "" {
		cause, detail := pinnedBlock(profiles, pinned, failures, now)
		return Choice{Name: ranked[0].Name, Cause: cause, Reason: fmt.Sprintf("pinned %s profile %s is unavailable: %s", role, pinned, detail)}, nil
	}
	if role == subagent.RoleReviewer {
		return Choice{Name: ranked[0].Name, Reason: "auto: best diagnostics + cost"}, nil
	}
	return Choice{Name: ranked[0].Name, Reason: "auto: best taste + cost"}, nil
}

// pinnedBlock says exactly why the role cannot use the profile the room pinned.
// It reads the same failure map and profile list as RankProfiles, so the owner
// sees the cause and can fix it instead of guessing.
func pinnedBlock(profiles []profile.Profile, profileName string, failures map[string]time.Time, now time.Time) (Cause, string) {
	for i := range profiles {
		p := profiles[i]
		if p.Name != profileName {
			continue
		}
		if p.Disabled {
			return CauseDisabled, "it is disabled"
		}
		if !p.ForServices() {
			return CauseNotForServices, "it does not allow the services use"
		}
		if cause, detail := blockReason(p, failures, now); cause != "" {
			return cause, detail
		}
		if adapter, err := runtime.Adapter(p.Harness); err == nil && adapter.Exclusive {
			pending, running := runtime.NewAccounts().SwitchStatus(adapter.Harness)
			if pending || running > 0 {
				return CauseHarnessBusy, fmt.Sprintf("its harness %s is switching accounts or running a turn", adapter.Harness)
			}
		}
		return "", "its profile record is incomplete"
	}
	return CauseMissing, "no profile has that name"
}

// NotReadyHeld reports whether a failure that stopped the runtime before the
// model ran still holds the profile out.
func NotReadyHeld(failures map[string]time.Time, profileName string, now time.Time) bool {
	return now.Before(failures[notReadyKey(profileName)])
}

// ClearNotReady drops the runtime-not-ready hold for profileName, because
// newer evidence shows the runtime starts. It returns an independent map.
func ClearNotReady(failures map[string]time.Time, profileName string) map[string]time.Time {
	out := MergeFailures(failures)
	delete(out, notReadyKey(profileName))
	return out
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
	if turn.Status == subagent.TurnError && unavailable(turn) {
		at := turn.Ended
		if at.IsZero() {
			at = now
		}
		if key, until := failureKey(info.Profile, turn), harness.UnavailableUntil(turn.Error, at); until.After(out[key]) {
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
		failed := t.Status == subagent.TurnError && unavailable(t)
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
				failures[failureKey(name, t)] = until
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

// quotaAnswer is the cached answer for one harness, provider and repo: whether
// every usable account is out of quota, and when the first one recovers.
type quotaAnswer struct {
	until time.Time
	held  bool
}

// quotaCache scans at most once per minute. The saved account limits change
// only when a turn fails over, so a minute-old view cannot hold a healthy
// profile out for long, and a hold whose reset passed frees the profile at
// once because the answer carries the reset time. Answers are keyed by repo,
// because the repo rules decide which accounts may run.
type quotaCache struct {
	mu      sync.Mutex
	checked time.Time
	answers map[string]quotaAnswer
}

var quotaCaches sync.Map

// AccountQuotaHolds names each profile whose every account that may run it in
// repo is out of quota, keyed under quotaKey for MergeFailures. It shares one
// account read per data directory and repo per minute and never fails a
// caller: an unreadable account store answers nothing, so quota state alone
// never hides a profile.
func AccountQuotaHolds(profiles []profile.Profile, repo string, now time.Time) map[string]time.Time {
	accounts := runtime.NewAccounts()
	value, _ := quotaCaches.LoadOrStore(accounts.Root, &quotaCache{})
	c := value.(*quotaCache)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.answers == nil || now.Before(c.checked) || !now.Before(c.checked.Add(time.Minute)) {
		c.checked, c.answers = now, map[string]quotaAnswer{}
	}
	holds := map[string]time.Time{}
	for _, p := range profiles {
		adapter, err := runtime.Adapter(p.Harness)
		if err != nil {
			continue
		}
		provider := runtime.ProviderForModel(p.Harness, p.Model)
		key := adapter.Harness + "\x00" + string(provider) + "\x00" + repo
		answer, known := c.answers[key]
		if !known {
			answer.until, answer.held = accounts.QuotaHeldOut(p.Harness, provider, repo, now)
			c.answers[key] = answer
		}
		if answer.held {
			holds[quotaKey(p.Name)] = answer.until
		}
	}
	return holds
}
