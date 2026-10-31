package model

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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

func TestQuotaFailureLogDoesNotExposeUnknownKeys(t *testing.T) {
	var output bytes.Buffer
	old := log.Writer()
	log.SetOutput(&output)
	t.Cleanup(func() { log.SetOutput(old) })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "credentials") {
			_, _ = w.Write([]byte(`{"credentials":[{"id":"invented","auth_index":"index","provider":"claude","state":"unknown"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"status_code":500,"body":"{\"sensitive-secret\":42,\"limits\":[]}"}`))
	}))
	defer server.Close()
	q := (&Proxy{client: &http.Client{Transport: quotaTestTransport{server.URL, server.Client().Transport}}}).Quotas("key", quotaStore(t), time.Now)
	_, err := q.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "sensitive-secret") || !strings.Contains(output.String(), "limits") || !strings.Contains(output.String(), "500") {
		t.Fatalf("unsafe or incomplete log: %s", output.String())
	}
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

func TestQuotasThroughManagement(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "proxy.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	refreshFails := false
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer management-key" {
			t.Error("missing management authorization")
		}
		switch r.URL.Path {
		case "/dyna/credentials":
			_, _ = w.Write([]byte(`{"credentials":[{"id":"invented","auth_index":"index","provider":"claude","state":"unknown"}]}`))
		case "/v0/management/api-call":
			calls++
			if refreshFails {
				http.Error(w, `{"error":"auth token refresh failed"}`, http.StatusBadRequest)
				return
			}
			var req struct {
				Index  string            `json:"auth_index"`
				Method string            `json:"method"`
				URL    string            `json:"url"`
				Header map[string]string `json:"header"`
			}
			if json.NewDecoder(r.Body).Decode(&req) != nil || req.Index != "index" || req.Method != "GET" || req.URL != "https://api.anthropic.com/api/oauth/usage" || req.Header["Authorization"] != "Bearer $TOKEN$" {
				t.Error("invalid proxy request")
			}
			_ = json.NewEncoder(w).Encode(struct {
				Status int    `json:"status_code"`
				Body   string `json:"body"`
			}{200, `{"five_hour":{"utilization":25.5,"resets_at":"2026-10-31T01:00:00Z"},"seven_day_oauth_apps":{"utilization":42,"resets_at":"2026-11-06T01:00:00Z"},"limits":[{"kind":"weekly_scoped","percent":0,"scope":{"model":{"id":"opus"}}}]}`})
		default:
			t.Error("unexpected request")
		}
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	now := time.Date(2026, 10, 30, 23, 0, 0, 0, time.UTC)
	q := (&Proxy{client: unixClient(socket)}).Quotas("management-key", quotaStore(t), func() time.Time { return now })
	first, err := q.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Accounts) != 1 {
		t.Fatalf("accounts: %+v", first.Accounts)
	}
	account := first.Accounts[0]
	if account.Provider != "claude" || account.Label != "Personal" || account.Cause != "" || len(account.Windows) != 2 {
		t.Fatalf("account: %+v", account)
	}
	a, b := account.Windows[0], account.Windows[1]
	if a.Name != "five_hour" || a.UsedPercent != 25.5 || a.Seconds != 18000 || a.ResetsAt.Format(time.RFC3339) != "2026-10-31T01:00:00Z" || b.Name != "seven_day" || b.UsedPercent != 42 || b.Seconds != 604800 || b.ResetsAt.Format(time.RFC3339) != "2026-11-06T01:00:00Z" {
		t.Fatalf("windows: %+v", account.Windows)
	}
	now = now.Add(4 * time.Minute)
	second, err := q.Read(context.Background())
	if err != nil || len(second.Accounts) != 1 || calls != 1 {
		t.Fatalf("cache: %+v, calls %d, error %v", second, calls, err)
	}
	now = now.Add(2 * time.Minute)
	refreshFails = true
	third, err := q.Read(context.Background())
	if err != nil || len(third.Accounts) != 1 || third.Accounts[0].Cause != "sign_in_expired" || calls != 2 {
		t.Fatalf("refresh failure: %+v, calls %d, error %v", third, calls, err)
	}
}
