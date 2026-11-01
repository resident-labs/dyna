package engine

import (
	"slices"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

// searchAccounts keeps usable room-eligible credentials and finds the best
// cached headroom. Missing cache entries remain usable.
func searchAccounts(accounts []store.AccountBinding, eligible []string, provider contract.AccountProvider, cached func(contract.AccountID) (contract.AccountUsage, bool)) ([]string, float64, contract.WebSearchAttemptReason) {
	ids := []string{}
	best := -1.0
	exhausted := false
	failed := false
	for _, binding := range accounts {
		if binding.Account.Provider != provider || !slices.Contains(eligible, binding.ProxyID) {
			continue
		}
		usage, ok := cached(binding.Account.ID)
		if ok && usage.Cause == contract.UsageSignInExpired {
			continue
		}
		// Other providers' usage endpoints differ from their model and search paths, so a
		// failed read there leaves the account usable with unknown headroom.
		if ok && provider == contract.ProviderAnthropic && usage.Cause == contract.UsageUnreachable {
			failed = true
			continue
		}
		worst := 0.0
		for _, window := range usage.Windows {
			// A limit on one model does not limit search on another.
			if window.Model == "" || provider == contract.ProviderAnthropic {
				worst = max(worst, window.UsedPercent)
			}
		}
		if ok && worst >= 100 {
			exhausted = true
			continue
		}
		ids = append(ids, binding.ProxyID)
		headroom := -1.0
		if ok && len(usage.Windows) != 0 {
			headroom = 100 - worst
		}
		best = max(best, headroom)
	}
	if len(ids) != 0 {
		return ids, best, ""
	}
	if exhausted {
		return nil, 0, contract.WebSearchNoQuota
	}
	if failed {
		return nil, 0, contract.WebSearchFailed
	}
	return nil, 0, contract.WebSearchNotSignedIn
}
