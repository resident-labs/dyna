package model

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

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
		{"oauth apps preferred", `[{"kind":"weekly_all","utilization":42},{"kind":"weekly_scoped","scope":"Oauth Apps","utilization":21},{"kind":"weekly_scoped","scope":"Opus","utilization":11}]`, 21},
		{"weekly fallback", `[{"kind":"weekly_all","utilization":42}]`, 42},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v0/management/auth-files":
					fmt.Fprint(w, `{"files":[{"auth_index":"invented","provider":"claude","label":"Personal"}]}`)
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
			q := (&Proxy{client: &http.Client{Transport: quotaTestTransport{server.URL, server.Client().Transport}}}).Quotas("key", func() time.Time { return now })
			for i := 0; i < 2; i++ {
				got, err := q.Read(context.Background())
				if err != nil || len(got.Accounts) != 1 {
					t.Fatalf("read failed: %v", err)
				}
				account := got.Accounts[0]
				if account.Cause != contract.UsageOK || account.ObservedAt == nil || !account.ObservedAt.Equal(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)) || len(account.Windows) == 0 || account.Windows[0].Name != "seven_day" || account.Windows[0].UsedPercent != tc.weekly || account.Windows[0].Seconds != 604800 {
					t.Fatal("unexpected normalized result")
				}
				if tc.name == "oauth apps preferred" && (len(account.Windows) != 2 || account.Windows[1].Name != "Opus Weekly") {
					t.Fatal("scoped window missing")
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
			fmt.Fprint(w, `{"files":[{"auth_index":"invented","provider":"claude"}]}`)
		}
	}))
	defer server.Close()
	q := (&Proxy{client: &http.Client{Transport: quotaTestTransport{server.URL, server.Client().Transport}}}).Quotas("key", time.Now)
	got, err := q.Read(context.Background())
	if err != nil || len(got.Accounts) != 1 || got.Accounts[0].Cause != contract.UsageSignInExpired {
		t.Fatalf("expired sign-in not reported: %v", err)
	}
}

func TestCodexPassiveQuota(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"files":[{"provider":"codex","quota":{"observed_at":"2026-01-02T00:00:00Z","signals":{"X-Codex-Primary-Used-Percent":"42","X-Codex-Primary-Window-Minutes":"300","X-Codex-Primary-Reset-After-Seconds":"3600"}}},{"provider":"codex","quota":{"signals":{}}}]}`))
	}))
	defer server.Close()
	q := (&Proxy{client: &http.Client{Transport: quotaTestTransport{server.URL, server.Client().Transport}}}).Quotas("key", time.Now)
	got, err := q.Read(context.Background())
	if err != nil || len(got.Accounts) != 2 {
		t.Fatalf("read failed: %v", err)
	}
	a := got.Accounts[0]
	if a.ObservedAt == nil || a.ObservedAt.Format(time.RFC3339) != "2026-01-02T00:00:00Z" || len(a.Windows) != 1 || a.Windows[0].UsedPercent != 42 || a.Windows[0].Seconds != 18000 || !a.Windows[0].ResetsAt.Equal(a.ObservedAt.Add(time.Hour)) {
		t.Fatal("Codex snapshot did not preserve its observation")
	}
	if got.Accounts[1].ObservedAt != nil || len(got.Accounts[1].Windows) != 0 {
		t.Fatal("missing signal became an observation")
	}
}

type deadlineTransport struct{}

func (deadlineTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if strings.HasSuffix(r.URL.Path, "/auth-files") {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"files":[{"auth_index":"invented","provider":"claude"}]}`)), Header: make(http.Header)}, nil
	}
	<-r.Context().Done()
	return nil, r.Context().Err()
}

func TestQuotaDeadlineIsUnreachable(t *testing.T) {
	q := (&Proxy{client: &http.Client{Transport: deadlineTransport{}}}).Quotas("key", time.Now)
	ctx, cancel := context.WithDeadline(context.Background(), time.Unix(1, 0))
	defer cancel()
	got, err := q.Read(ctx)
	if err != nil || len(got.Accounts) != 1 || got.Accounts[0].Cause != "unreachable" {
		t.Fatalf("deadline did not report unreachable: %v", err)
	}
}
