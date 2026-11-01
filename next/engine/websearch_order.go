package engine

import (
	"slices"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/model"
)

// searchAccounts keeps only usable room-eligible credentials and finds the best
// known headroom. Unknown quota sorts behind known headroom.
func searchAccounts(credentials []model.Credential, eligible []string, provider string) ([]string, float64, contract.WebSearchAttemptReason) {
	ids := []string{}
	best := -1.0
	exhausted := false
	for _, c := range credentials {
		if c.Provider != provider || !slices.Contains(eligible, c.ID) || c.State == model.CredentialExpired || c.State == model.CredentialFailed {
			continue
		}
		worst := 0.0
		known := false
		for _, window := range []model.CredentialQuotaWindow{c.Quota.Primary, c.Quota.Secondary} {
			if window.UsedPercent != nil {
				known = true
				worst = max(worst, *window.UsedPercent)
			}
		}
		if known && worst >= 100 {
			exhausted = true
			continue
		}
		ids = append(ids, c.ID)
		headroom := -1.0
		if known {
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
	return nil, 0, contract.WebSearchNotSignedIn
}
