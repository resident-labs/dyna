package engine

import (
	"context"
	"database/sql"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
	"github.com/resident-labs/dyna/engine/websearch"
	"testing"
	"time"
)

type benchmarkSearchProvider string

func (p benchmarkSearchProvider) Name() string { return string(p) }
func (p benchmarkSearchProvider) Search(context.Context, websearch.Query) ([]websearch.Result, error) {
	return nil, nil
}

func BenchmarkWebSearchOrderToFirstProvider(b *testing.B) {
	now := time.Unix(1000, 0)
	service := &Service{now: func() time.Time { return now }, searchFailures: map[string]time.Time{"room/claude": now.Add(time.Minute)}}
	accounts := []store.AccountBinding{{ProxyID: "id", Account: contract.Account{ID: "account", Provider: contract.ProviderOpenAI}}}
	cached := func(contract.AccountID) (contract.AccountUsage, bool) {
		return contract.AccountUsage{Windows: []contract.UsageWindow{{UsedPercent: 20}}}, true
	}
	eligible := []string{"id"}
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		_, headroom, _ := searchAccounts(accounts, eligible, contract.ProviderOpenAI, cached)
		candidates := []searchCandidate{{provider: benchmarkSearchProvider("codex"), headroom: headroom}, {provider: benchmarkSearchProvider("claude")}, {provider: benchmarkSearchProvider("brave")}}
		service.orderSearchCandidates("room", candidates, nil)
		_, _ = candidates[0].provider.Search(ctx, websearch.Query{Text: "query"})
	}
}

func BenchmarkWebSearchOrderWithStore(b *testing.B) {
	st, err := store.Open(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	if err := st.SetSearchKey(ctx, "brave", "benchmark-key"); err != nil {
		b.Fatal(err)
	}
	if err := st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO accounts(id,proxy_id,provider,label) VALUES('account','proxy','openai','benchmark')")
		return err
	}); err != nil {
		b.Fatal(err)
	}
	now := time.Unix(1000, 0)
	service := &Service{now: func() time.Time { return now }}
	cached := func(contract.AccountID) (contract.AccountUsage, bool) { return contract.AccountUsage{}, false }
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		accounts, err := st.Accounts(ctx)
		if err != nil {
			b.Fatal(err)
		}
		eligible, err := st.EligibleAccountProxyIDs(ctx, "room")
		if err != nil {
			b.Fatal(err)
		}
		_, err = st.SearchKey(ctx, "brave")
		if err != nil {
			b.Fatal(err)
		}
		_, headroom, _ := searchAccounts(accounts, eligible, contract.ProviderOpenAI, cached)
		candidates := []searchCandidate{{provider: benchmarkSearchProvider("chatgpt"), headroom: headroom}, {provider: benchmarkSearchProvider("brave")}}
		service.orderSearchCandidates("room", candidates, nil)
		_, _ = candidates[0].provider.Search(ctx, websearch.Query{Text: "query"})
	}
}

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

func TestSearchAccountsIgnoresFailedUsageReadsOutsideClaude(t *testing.T) {
	accounts := []store.AccountBinding{{ProxyID: "kimi", Account: contract.Account{ID: "a", Provider: contract.ProviderKimi}}}
	for _, cause := range []contract.UsageCause{contract.UsageUnreachable, contract.UsageRateLimited} {
		cached := func(contract.AccountID) (contract.AccountUsage, bool) {
			return contract.AccountUsage{Cause: cause}, true
		}
		if ids, headroom, reason := searchAccounts(accounts, []string{"kimi"}, contract.ProviderKimi, cached); len(ids) != 1 || headroom != -1 || reason != "" {
			t.Fatalf("%s: %v %v %q", cause, ids, headroom, reason)
		}
	}
	claude := []store.AccountBinding{{ProxyID: "claude", Account: contract.Account{ID: "a", Provider: contract.ProviderAnthropic}}}
	cached := func(contract.AccountID) (contract.AccountUsage, bool) {
		return contract.AccountUsage{Cause: contract.UsageUnreachable}, true
	}
	if _, _, reason := searchAccounts(claude, []string{"claude"}, contract.ProviderAnthropic, cached); reason != contract.WebSearchFailed {
		t.Fatalf("reason %q", reason)
	}
}

func TestSearchAccountsIgnoresModelLimitsOutsideClaude(t *testing.T) {
	accounts := []store.AccountBinding{{ProxyID: "codex", Account: contract.Account{ID: "a", Provider: contract.ProviderOpenAI}}}
	cached := func(contract.AccountID) (contract.AccountUsage, bool) {
		return contract.AccountUsage{Windows: []contract.UsageWindow{{UsedPercent: 20}, {Model: "Spark", UsedPercent: 100}}}, true
	}
	if ids, headroom, reason := searchAccounts(accounts, []string{"codex"}, contract.ProviderOpenAI, cached); len(ids) != 1 || headroom != 80 || reason != "" {
		t.Fatalf("%v %v %q", ids, headroom, reason)
	}
}
