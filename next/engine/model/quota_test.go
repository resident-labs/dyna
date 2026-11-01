package model

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

func quotaStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	if err := st.SaveProxyAuth(ctx, store.ProxyAuth{ID: "invented", Provider: "claude", Record: []byte("{}")}, true); err != nil {
		t.Fatal(err)
	}
	if err := st.BindAccount(ctx, "account", "invented", contract.ProviderAnthropic, "Personal"); err != nil {
		t.Fatal(err)
	}
	return st
}

type quotaTestTransport struct {
	url  string
	base http.RoundTripper
}

func (t quotaTestTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r.URL.Scheme = "http"
	r.URL.Host = strings.TrimPrefix(t.url, "http://")
	return t.base.RoundTrip(r)
}

func TestQuotaNormalizedWindowsAndCache(t *testing.T) {
	for _, tc := range []struct {
		name, windows string
		weekly        float64
	}{
		{"oauth apps preferred", `[{"kind":"session","utilization":17},{"kind":"weekly_all","utilization":42},{"kind":"weekly_scoped","scope":"Oauth Apps","utilization":21},{"kind":"weekly_scoped","scope":"Opus","utilization":11}]`, 21},
		{"weekly fallback", `[{"kind":"weekly_all","utilization":42}]`, 42},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/dyna/credentials":
					fmt.Fprint(w, `{"credentials":[{"id":"invented","auth_index":"invented","provider":"claude","state":"unknown"}]}`)
				case "/v0/management/auth-files/quota":
					calls++
					if r.URL.Query().Get("auth_index") != "invented" {
						t.Error("wrong account filter")
					}
					fmt.Fprintf(w, `{"files":[{"auth_index":"invented","observed_at":"2026-01-01T00:00:00Z","status_code":200,"windows":%s}]}`, tc.windows)
				default:
					t.Error("unexpected request")
				}
			}))
			defer server.Close()
			now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			q := (&Proxy{client: &http.Client{Transport: quotaTestTransport{server.URL, server.Client().Transport}}}).Quotas("key", quotaStore(t), func() time.Time { return now })
			for i := 0; i < 2; i++ {
				got, err := q.Read(context.Background())
				if err != nil || len(got.Accounts) != 1 {
					t.Fatalf("read failed: %v", err)
				}
				account := got.Accounts[0]
				if account.Cause != contract.UsageOK || account.ObservedAt == nil || !account.ObservedAt.Equal(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)) || len(account.Windows) == 0 || account.Windows[len(account.Windows)-1].Seconds != 604800 {
					t.Fatal("unexpected normalized result")
				}
				if tc.name == "oauth apps preferred" {
					if len(account.Windows) != 3 || account.Windows[0].Kind != contract.UsageWindowFiveHour || account.Windows[1].Kind != contract.UsageWindowWeekly || account.Windows[1].UsedPercent != tc.weekly || account.Windows[2].Model != "Opus" {
						t.Fatal("wrong typed window order or scoped model")
					}
				} else if account.Windows[0].Kind != contract.UsageWindowWeekly || account.Windows[0].UsedPercent != tc.weekly {
					t.Fatal("weekly fallback missing")
				}
			}
			if calls != 1 {
				t.Fatalf("quota requests = %d, want 1", calls)
			}
			now = now.Add(6 * time.Minute)
			if _, err := q.Read(context.Background()); err != nil || calls != 2 {
				t.Fatalf("cache did not expire: calls = %d, error = %v", calls, err)
			}
		})
	}
}

func TestQuotaExpiredSignIn(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/quota") {
			fmt.Fprint(w, `{"files":[{"auth_index":"invented","status_code":401,"error":"upstream returned status 401"}]}`)
		} else {
			fmt.Fprint(w, `{"credentials":[{"id":"invented","auth_index":"invented","provider":"claude","state":"unknown"}]}`)
		}
	}))
	defer server.Close()
	q := (&Proxy{client: &http.Client{Transport: quotaTestTransport{server.URL, server.Client().Transport}}}).Quotas("key", quotaStore(t), time.Now)
	got, err := q.Read(context.Background())
	if err != nil || len(got.Accounts) != 1 || got.Accounts[0].Cause != contract.UsageSignInExpired {
		t.Fatalf("expired sign-in not reported: %v", err)
	}
}

func quotaAccounts(t *testing.T, provider contract.AccountProvider, ids ...string) *store.Store {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	for _, id := range ids {
		if err := st.SaveProxyAuth(ctx, store.ProxyAuth{ID: id, Provider: string(provider), Record: []byte("{}")}, true); err != nil {
			t.Fatal(err)
		}
		if err := st.BindAccount(ctx, contract.AccountID("account-"+id), id, provider, "Label "+id); err != nil {
			t.Fatal(err)
		}
	}
	return st
}

func quotaProxy(url string, base http.RoundTripper) *Proxy {
	return &Proxy{client: &http.Client{Transport: quotaTestTransport{url, base}}}
}

func TestQuotaReadsEveryProviderThroughOneEndpoint(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/dyna/credentials" {
			fmt.Fprint(w, `{"credentials":[{"id":"invented","auth_index":"index","provider":"kimi","state":"unknown"}]}`)
			return
		}
		calls++
		fmt.Fprint(w, `{"files":[{"auth_index":"index","observed_at":"2026-01-02T00:00:00Z","status_code":200,"state":"ok","windows":[
			{"kind":"monthly","name":"monthly","seconds":2592000,"utilization":55.31,"resets_at":"2026-01-22T00:00:00Z"},
			{"kind":"five_hour","name":"five_hour","seconds":18000,"utilization":10},
			{"kind":"monthly","name":"Code monthly","scope":"Code","seconds":2592000,"utilization":1}],
			"balances":[{"currency":"USD","amount":"12.34"}]}]}`)
	}))
	defer server.Close()
	now := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	q := quotaProxy(server.URL, server.Client().Transport).Quotas("key", quotaAccounts(t, contract.ProviderKimi, "invented"), func() time.Time { return now })
	got, err := q.Read(context.Background())
	if err != nil || len(got.Accounts) != 1 {
		t.Fatalf("read failed: %v", err)
	}
	a := got.Accounts[0]
	if a.Cause != contract.UsageOK || a.ObservedAt == nil || a.Label != "Label invented" || len(a.Windows) != 3 || a.Windows[0].Kind != contract.UsageWindowFiveHour || a.Windows[0].ResetsAt != (time.Time{}) {
		t.Fatalf("usage = %#v", a)
	}
	if monthly := a.Windows[1]; monthly.Kind != contract.UsageWindowMonthly || monthly.UsedPercent != 55.31 || monthly.Seconds != 2592000 || a.Windows[2].Model != "Code" {
		t.Fatalf("windows = %#v", a.Windows)
	}
	if len(a.Balances) != 1 || a.Balances[0] != (contract.UsageBalance{Currency: "USD", Amount: "12.34"}) {
		t.Fatalf("balances = %#v", a.Balances)
	}
	if cached, ok := q.CachedUsage("account-invented"); !ok || len(cached.Windows) != 3 {
		t.Fatal("the reading was not cached under its account")
	}
	if q.ObservedState("index") != CredentialConnected {
		t.Fatal("a successful read did not show the sign-in works")
	}
	if _, err := q.Read(context.Background()); err != nil || calls != 1 {
		t.Fatalf("cache missed: calls = %d, error = %v", calls, err)
	}
	now = now.Add(6 * time.Minute)
	if _, err := q.Read(context.Background()); err != nil || calls != 2 {
		t.Fatalf("cache did not expire: calls = %d, error = %v", calls, err)
	}
}

func TestQuotaReaderFailuresNeverExpireASignIn(t *testing.T) {
	for _, tc := range []struct {
		name, file string
		cause      contract.UsageCause
	}{
		{"unauthorized", `{"auth_index":"index","status_code":401,"state":"unreachable","error":"upstream returned status 401"}`, contract.UsageUnreachable},
		{"forbidden", `{"auth_index":"index","status_code":403,"state":"unreachable"}`, contract.UsageUnreachable},
		{"rate limited", `{"auth_index":"index","status_code":429,"state":"rate_limited"}`, contract.UsageRateLimited},
		{"unknown", `{"auth_index":"index","status_code":200,"state":"unknown"}`, contract.UsageOK},
		{"new state", `{"auth_index":"index","status_code":200,"state":"invented"}`, contract.UsageUnreachable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/dyna/credentials" {
					fmt.Fprint(w, `{"credentials":[{"id":"invented","auth_index":"index","provider":"kimi","state":"unknown"}]}`)
					return
				}
				fmt.Fprintf(w, `{"files":[%s]}`, tc.file)
			}))
			defer server.Close()
			q := quotaProxy(server.URL, server.Client().Transport).Quotas("key", quotaAccounts(t, contract.ProviderKimi, "invented"), time.Now)
			got, err := q.Read(context.Background())
			if err != nil || len(got.Accounts) != 1 {
				t.Fatalf("read failed: %v", err)
			}
			if a := got.Accounts[0]; a.Cause != tc.cause || len(a.Windows) != 0 {
				t.Fatalf("usage = %#v", a)
			}
			if q.ObservedState("index") != CredentialUnknown {
				t.Fatal("a reading without windows changed the account state")
			}
		})
	}
}

func TestQuotaBoundsConcurrentReadsAndSharesThem(t *testing.T) {
	const accounts = 9
	ids := make([]string, accounts)
	var list strings.Builder
	for i := range ids {
		ids[i] = fmt.Sprintf("invented-%d", i)
		if i > 0 {
			list.WriteByte(',')
		}
		fmt.Fprintf(&list, `{"id":%q,"auth_index":%q,"provider":"kimi","state":"unknown"}`, ids[i], ids[i])
	}
	var active, peak, reads atomic.Int32
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/dyna/credentials" {
			fmt.Fprintf(w, `{"credentials":[%s]}`, list.String())
			return
		}
		reads.Add(1)
		now := active.Add(1)
		defer active.Add(-1)
		for {
			seen := peak.Load()
			if now <= seen || peak.CompareAndSwap(seen, now) {
				break
			}
		}
		<-release
		fmt.Fprintf(w, `{"files":[{"auth_index":%q,"status_code":200,"state":"unknown"}]}`, r.URL.Query().Get("auth_index"))
	}))
	defer server.Close()
	q := quotaProxy(server.URL, server.Client().Transport).Quotas("key", quotaAccounts(t, contract.ProviderKimi, ids...), time.Now)
	var callers sync.WaitGroup
	for range 3 {
		callers.Go(func() {
			if got, err := q.Read(context.Background()); err != nil || len(got.Accounts) != accounts {
				t.Errorf("read failed: %v", err)
			}
		})
	}
	for peak.Load() < quotaReads {
		time.Sleep(time.Millisecond)
	}
	// Let all three callers reach every credential before any read can finish.
	time.Sleep(50 * time.Millisecond)
	close(release)
	callers.Wait()
	if peak.Load() != quotaReads {
		t.Fatalf("peak = %d, want %d", peak.Load(), quotaReads)
	}
	if reads.Load() != accounts {
		t.Fatalf("reads = %d, want one per account", reads.Load())
	}
}

func TestQuotaCanceledCallerDoesNotCacheAFailure(t *testing.T) {
	calls := 0
	started, release := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/dyna/credentials" {
			fmt.Fprint(w, `{"credentials":[{"id":"invented","auth_index":"index","provider":"kimi","state":"unknown"}]}`)
			return
		}
		calls++
		close(started)
		<-release
		fmt.Fprint(w, `{"files":[{"auth_index":"index","status_code":200,"state":"ok","windows":[{"kind":"weekly","name":"seven_day","seconds":604800,"utilization":3}]}]}`)
	}))
	defer server.Close()
	q := quotaProxy(server.URL, server.Client().Transport).Quotas("key", quotaAccounts(t, contract.ProviderKimi, "invented"), time.Now)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _, _ = q.Read(ctx); close(done) }()
	<-started
	cancel()
	close(release)
	<-done
	if cached, ok := q.CachedUsage("account-invented"); !ok || cached.Cause != contract.UsageOK || len(cached.Windows) != 1 {
		t.Fatalf("cached = %#v", cached)
	}
}
