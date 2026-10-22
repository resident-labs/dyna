package state

import (
	"strings"
	"time"
)

func (s RateLimitScope) Normalize() RateLimitScope {
	return RateLimitScope{strings.ToLower(strings.TrimSuffix(s.Host, ".")), strings.ToLower(s.Account), strings.ToLower(s.Resource)}
}
func (s RateLimitScope) Key() string {
	s = s.Normalize()
	return s.Host + "\n" + s.Account + "\n" + s.Resource
}
func (s RateLimitScope) Known() bool { return s.Host != "" && s.Account != "" && s.Resource != "" }

// TODO: legacy/shim remove later — migrates pauses with known provenance; unknown mixed pauses expire at their original deadline.
func (st *State) ImportLegacyQuota(rate RateLimit) {
	if rate.PauseUntil.IsZero() {
		return
	}
	scope := rate.PauseScope.Normalize()
	if scope.Known() {
		if st.RateLimits == nil {
			st.RateLimits = map[string]RateLimit{}
		}
		if _, found := st.RateLimits[scope.Key()]; !found {
			st.RateLimits[scope.Key()] = RateLimit{PauseUntil: rate.PauseUntil, Reason: rate.Reason, PauseScope: scope}
		}
	} else if rate.PauseUntil.After(st.LegacyPause) {
		st.LegacyPause = rate.PauseUntil
	}
}

// Quota combines display and scheduling facts only for the given account and host.
// The entries remain independent; this result is never used for accounting.
func (st State) Quota(host, account string, now time.Time) RateLimit {
	result := RateLimit{PauseUntil: st.LegacyPause}
	for _, resource := range []string{"core", "graphql"} {
		rate := st.RateLimits[(RateLimitScope{host, account, resource}).Key()]
		if !now.IsZero() && !now.Before(rate.Reset) {
			rate.Remaining = nil
		}
		combineQuota(&result, rate)
	}
	return result
}
func combineQuota(result *RateLimit, rate RateLimit) {
	if rate.Remaining != nil && (result.Remaining == nil || *rate.Remaining < *result.Remaining || *rate.Remaining == *result.Remaining && rate.Reset.Before(result.Reset)) {
		result.Remaining = rate.Remaining
		result.Reset = rate.Reset
	}
	if rate.PauseUntil.After(result.PauseUntil) {
		result.PauseUntil = rate.PauseUntil
		result.Reason = rate.Reason
		result.PauseScope = rate.PauseScope
	}
}
func (st *State) RefreshQuotaDisplay() {
	st.RateLimit = RateLimit{PauseUntil: st.LegacyPause}
	for _, rate := range st.RateLimits {
		combineQuota(&st.RateLimit, rate)
	}
	for _, r := range st.Records {
		if r != nil {
			r.RateLimit = st.Quota(r.Host, r.QuotaAccount, time.Time{})
		}
	}
}

// TODO: legacy/shim remove later — expires the conservative pause from old unscoped data at its saved deadline.
func (st *State) ExpireLegacyPause(now time.Time) {
	if !now.Before(st.LegacyPause) {
		st.LegacyPause = time.Time{}
	}
}
