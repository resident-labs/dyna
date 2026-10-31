package model

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestCodexPassiveQuota(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"credentials":[{"id":"invented","auth_index":"invented","provider":"codex","state":"unknown","quota":{"observed_at":"2026-01-02T00:00:00Z","primary":{"used_percent":20,"window_minutes":10080},"secondary":{"used_percent":42,"window_minutes":300,"reset_after_seconds":3600}}}]}`))
	}))
	defer server.Close()
	q := (&Proxy{client: &http.Client{Transport: quotaTestTransport{server.URL, server.Client().Transport}}}).Quotas("key", quotaStore(t), time.Now)
	got, err := q.Read(context.Background())
	if err != nil || len(got.Accounts) != 1 {
		t.Fatalf("read failed: %v", err)
	}
	a := got.Accounts[0]
	if a.Provider != contract.ProviderAnthropic || a.ObservedAt == nil || a.ObservedAt.Format(time.RFC3339) != "2026-01-02T00:00:00Z" || len(a.Windows) != 2 || a.Windows[0].UsedPercent != 42 || a.Windows[0].Kind != contract.UsageWindowFiveHour || a.Windows[0].Seconds != 18000 || !a.Windows[0].ResetsAt.Equal(a.ObservedAt.Add(time.Hour)) {
		t.Fatal("Codex snapshot did not preserve its observation")
	}

}
