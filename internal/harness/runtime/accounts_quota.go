package runtime

import (
	"time"

	"dyna-agent/internal/account/store"
)

// QuotaAvailability describes the evidence for accounts permitted in a repo.
type QuotaAvailability string

const (
	QuotaAvailable   QuotaAvailability = "available"
	QuotaUnavailable QuotaAvailability = "unavailable"
	QuotaUnknown     QuotaAvailability = "unknown"
)

// QuotaEvidence records availability and its saved evidence time.
type QuotaEvidence struct {
	State      QuotaAvailability
	ObservedAt time.Time
}

// QuotaAvailability reads saved account evidence without a cache.
func (a *Accounts) QuotaAvailability(harness string, provider store.Provider, repo string, now time.Time) (QuotaEvidence, error) {
	unknown := QuotaEvidence{State: QuotaUnknown}
	h, err := canonicalAccountHarness(harness)
	if err != nil {
		return unknown, err
	}
	if provider == "" {
		return unknown, nil
	}
	scopes, err := a.scopes()
	if err != nil {
		return unknown, err
	}
	limits, err := scopes.Limits(now)
	if err != nil {
		return unknown, err
	}
	recorded, err := scopes.LimitRecordedAt(now)
	if err != nil {
		return unknown, err
	}
	rules, err := scopes.Rules()
	if err != nil {
		return unknown, err
	}
	all, problems, err := a.ListWithProblems()
	if err != nil {
		return unknown, err
	}
	var open []NativeAccount
	var ids []string
	for _, n := range all {
		if accountRuns(h, n, provider) && n.Status == "ready" && rules.Allows(n.ID, repo) {
			open = append(open, n)
			ids = append(ids, n.ID)
		}
	}
	if len(open) == 0 {
		return unknown, nil
	}
	readings, err := a.readObservations(ids)
	if err != nil {
		return unknown, err
	}
	uncertain := len(problems) > 0
	var evidence time.Time
	for _, n := range open {
		o := readings[n.ID]
		if _, limited := limits[n.ID]; limited && !readingHasQuotaLeft(o, recorded[n.ID], now) {
			if recorded[n.ID].After(evidence) {
				evidence = recorded[n.ID]
			}
			continue
		}
		if o == nil || o.Unavailable || o.ObservedAt.IsZero() || o.ObservedAt.After(now) || now.Sub(o.ObservedAt) > store.SavedUsageWindow {
			uncertain = true
			continue
		}
		if _, spent := readingOutOfQuota(o, now); spent {
			if o.ObservedAt.After(evidence) {
				evidence = o.ObservedAt
			}
			continue
		}
		if !readingHasQuotaLeft(o, time.Unix(0, 1), now) {
			uncertain = true
			continue
		}
		return QuotaEvidence{State: QuotaAvailable, ObservedAt: o.ObservedAt}, nil
	}
	if uncertain {
		return unknown, nil
	}
	return QuotaEvidence{State: QuotaUnavailable, ObservedAt: evidence}, nil
}

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

// readingOutOfQuota applies the shared eligibility decision and returns the
// earliest future reset named by an ineligible reading. A failed read keeps the
// windows of an older reading, so it names only its own rate-limit end.
func readingOutOfQuota(o *store.Observation, now time.Time) (time.Time, bool) {
	if o != nil && o.Unavailable {
		if o.RateLimitedUntil != nil && o.RateLimitedUntil.After(now) {
			return *o.RateLimitedUntil, true
		}
		return time.Time{}, false
	}
	if store.DecideUsage(o, now).Eligible {
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
