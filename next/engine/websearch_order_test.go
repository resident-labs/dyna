package engine

import (
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
	"testing"
)

func TestSearchAccountsUsesEligibleHeadroom(t *testing.T) {
	accounts := []store.AccountBinding{
		{ProxyID: "best", Account: contract.Account{ID: "a", Provider: contract.ProviderOpenAI}},
		{ProxyID: "limited", Account: contract.Account{ID: "b", Provider: contract.ProviderOpenAI}},
		{ProxyID: "expired", Account: contract.Account{ID: "c", Provider: contract.ProviderOpenAI}},
	}
	usage := map[contract.AccountID]contract.AccountUsage{
		"a": {Windows: []contract.UsageWindow{{UsedPercent: 10}}},
		"b": {Windows: []contract.UsageWindow{{UsedPercent: 90}, {UsedPercent: 100}}},
		"c": {Cause: contract.UsageSignInExpired},
	}
	cached := func(id contract.AccountID) (contract.AccountUsage, bool) { u, ok := usage[id]; return u, ok }
	ids, headroom, reason := searchAccounts(accounts, []string{"best", "limited", "expired"}, contract.ProviderOpenAI, cached)
	if len(ids) != 1 || ids[0] != "best" || headroom != 90 || reason != "" {
		t.Fatalf("%v %v %q", ids, headroom, reason)
	}
	_, _, reason = searchAccounts(accounts, []string{"limited"}, contract.ProviderOpenAI, cached)
	if reason != contract.WebSearchNoQuota {
		t.Fatalf("reason %q", reason)
	}
	_, _, reason = searchAccounts(accounts, []string{"expired"}, contract.ProviderOpenAI, cached)
	if reason != contract.WebSearchNotSignedIn {
		t.Fatalf("reason %q", reason)
	}
}
