package runtime

import (
	"time"

	"dyna-agent/internal/account/store"
)

// QuotaHeldOut reports when every account that may run harness h for provider
// p is out of quota: the saved usage limit of each usable account ends after
// now. An account failover records those limits after a real quota rejection,
// so a ranking that skips such a harness does not first spend a turn on the
// same rejection. It returns false when at least one usable account has quota
// left, and when no usable account is known, so the caller's own failure
// records keep deciding those. A read error also returns false: unreadable
// quota state never hides a profile.
func (a *Accounts) QuotaHeldOut(harness string, provider store.Provider, now time.Time) (time.Time, bool) {
	h, err := canonicalAccountHarness(harness)
	if err != nil || provider == "" {
		return time.Time{}, false
	}
	scopes, err := a.scopes()
	if err != nil {
		return time.Time{}, false
	}
	limits, err := scopes.Limits(now)
	if err != nil || len(limits) == 0 {
		return time.Time{}, false
	}
	all, err := a.List()
	if err != nil {
		return time.Time{}, false
	}
	usable, earliest := 0, time.Time{}
	for _, n := range all {
		if !accountRuns(h, n, provider) || n.Status != "ready" {
			continue
		}
		usable++
		if until, limited := limits[n.ID]; !limited {
			return time.Time{}, false
		} else if earliest.IsZero() || until.Before(earliest) {
			earliest = until
		}
	}
	if usable == 0 {
		return time.Time{}, false
	}
	return earliest, true
}
