package model

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"
)

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
		case "/v0/management/auth-files":
			_, _ = w.Write([]byte(`{"files":[{"auth_index":"index","provider":"claude","label":"Personal"}]}`))
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
			}{200, `{"five_hour":{"utilization":25.5,"resets_at":"2026-10-31T01:00:00Z"},"seven_day_oauth_apps":{"utilization":42,"resets_at":"2026-11-06T01:00:00Z"}}`})
		default:
			t.Error("unexpected request")
		}
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	now := time.Date(2026, 10, 30, 23, 0, 0, 0, time.UTC)
	q := (&Proxy{client: unixClient(socket)}).Quotas("management-key", func() time.Time { return now })
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
