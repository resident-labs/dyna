package runtime

import (
	"time"

	"dyna-agent/internal/account/store"
)

// QuotaHeldOut reports when every account that may run harness h for provider
// p in repo is out of quota. A saved failover limit counts, and so does a
// saved usage reading that says the account has no quota left. A ranking that
// skips such a harness does not first spend a turn on the same rejection. A
// usable account is signed in, serves the harness and provider, and the repo
// rules allow it in repo, exactly as SelectForRepo and Failover decide. It
// returns false when at least one usable account has quota left, and when no
// usable account is known, so the caller's own failure records keep deciding
// those. A read error also returns false: unreadable quota state never hides
// a profile.
func (a *Accounts) QuotaHeldOut(harness string, provider store.Provider, repo string, now time.Time) (time.Time, bool) {
	h, err := canonicalAccountHarness(harness)
	if err != nil || provider == "" {
		return time.Time{}, false
	}
	scopes, err := a.scopes()
	if err != nil {
		return time.Time{}, false
	}
	limits, err := scopes.Limits(now)
	if err != nil {
		return time.Time{}, false
	}
	if limits == nil {
		limits = map[string]time.Time{}
	}
	recorded, err := scopes.LimitRecordedAt(now)
	if err != nil {
		return time.Time{}, false
	}
	rules, err := scopes.Rules()
	if err != nil {
		return time.Time{}, false
	}
	all, err := a.List()
	if err != nil {
		return time.Time{}, false
	}
	var open []NativeAccount
	for _, n := range all {
		if accountRuns(h, n, provider) && n.Status == "ready" && rules.Allows(n.ID, repo) {
			open = append(open, n)
		}
	}
	if len(open) == 0 {
		return time.Time{}, false
	}
	ids := make([]string, len(open))
	for i, n := range open {
		ids[i] = n.ID
	}
	readings, _ := a.readObservations(ids)
	earliest := time.Time{}
	for _, n := range open {
		until, blocked := time.Time{}, false
		if end, limited := limits[n.ID]; limited && !readingHasQuotaLeft(readings[n.ID], recorded[n.ID], now) {
			until, blocked = end, true
		} else if end, spent := readingOutOfQuota(readings[n.ID], now); spent {
			until, blocked = end, true
		}
		if !blocked {
			return time.Time{}, false
		}
		if earliest.IsZero() || (!until.IsZero() && until.Before(earliest)) {
			earliest = until
		}
	}
	if earliest.IsZero() {
		earliest = now.Add(time.Minute)
	}
	return earliest, true
}

// readingHasQuotaLeft reports that a reading captured after the failure
// positively refutes its hold. Every reported session or weekly window must
// have quota left; a reading without either window says nothing about the hold.
func readingHasQuotaLeft(o *store.Observation, failedAt, now time.Time) bool {
	if o == nil || failedAt.IsZero() || !o.ObservedAt.After(failedAt) || o.Unavailable || o.RateLimitedUntil != nil && o.RateLimitedUntil.After(now) {
		return false
	}
	session, weekly := store.SessionWindow(o), store.WeeklyWindow(o)
	if session == nil && weekly == nil {
		return false
	}
	for _, window := range []*store.Window{session, weekly} {
		if window != nil && (window.UsedPercent == nil || *window.UsedPercent >= store.NearCapUsedPercent) {
			return false
		}
	}
	return true
}

// readingOutOfQuota reports that a saved usage reading has no quota left, using
// the same rule as chooseUsage. A missing reading, or one with no weekly
// window, is pickable. The time is the earliest reset the reading names.
func readingOutOfQuota(o *store.Observation, now time.Time) (time.Time, bool) {
	if o == nil || store.WeeklyWindow(o) == nil {
		return time.Time{}, false
	}
	blocked := o.Unavailable || (o.RateLimitedUntil != nil && o.RateLimitedUntil.After(now))
	if w := store.SessionWindow(o); !blocked && w != nil && w.UsedPercent != nil && *w.UsedPercent >= store.NearCapUsedPercent {
		blocked = true
	}
	if w := store.WeeklyWindow(o); !blocked && w.UsedPercent != nil && *w.UsedPercent >= store.NearCapUsedPercent {
		blocked = true
	}
	if !blocked {
		return time.Time{}, false
	}
	at := time.Time{}
	if o.RateLimitedUntil != nil && o.RateLimitedUntil.After(now) {
		at = *o.RateLimitedUntil
	}
	for _, w := range o.Windows {
		reset, err := time.Parse(time.RFC3339Nano, w.ResetsAt)
		if err == nil && reset.After(now) && (at.IsZero() || reset.Before(at)) {
			at = reset
		}
	}
	return at, true
}
