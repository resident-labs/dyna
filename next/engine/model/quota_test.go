package model

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestQuotaFailureLogDoesNotExposeUnknownKeys(t *testing.T) {
	var output bytes.Buffer
	old := log.Writer()
	log.SetOutput(&output)
	t.Cleanup(func() { log.SetOutput(old) })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "auth-files") {
			_, _ = w.Write([]byte(`{"files":[{"auth_index":"index","provider":"claude"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"status_code":500,"body":"{\"sensitive-secret\":42,\"limits\":[]}"}`))
	}))
	defer server.Close()
	q := (&Proxy{client: &http.Client{Transport: quotaTestTransport{server.URL, server.Client().Transport}}}).Quotas("key", time.Now)
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
			}{200, `{"five_hour":{"utilization":25.5,"resets_at":"2026-10-31T01:00:00Z"},"seven_day_oauth_apps":{"utilization":42,"resets_at":"2026-11-06T01:00:00Z"},"limits":[{"kind":"weekly_scoped","percent":0,"scope":{"model":{"id":"opus"}}}]}`})
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
	if account.Provider != "claude" || account.Label != "Personal" || account.Cause != "" || account.ObservedAt == nil || !account.ObservedAt.Equal(now) || len(account.Windows) != 2 {
		t.Fatalf("account: %+v", account)
	}
	a, b := account.Windows[0], account.Windows[1]
	if a.Name != "five_hour" || a.UsedPercent != 25.5 || a.Seconds != 18000 || a.ResetsAt.Format(time.RFC3339) != "2026-10-31T01:00:00Z" || b.Name != "seven_day" || b.UsedPercent != 42 || b.Seconds != 604800 || b.ResetsAt.Format(time.RFC3339) != "2026-11-06T01:00:00Z" {
		t.Fatalf("windows: %+v", account.Windows)
	}
	now = now.Add(4 * time.Minute)
	second, err := q.Read(context.Background())
	if err != nil || len(second.Accounts) != 1 || second.Accounts[0].ObservedAt == nil || !second.Accounts[0].ObservedAt.Equal(*account.ObservedAt) || calls != 1 {
		t.Fatalf("cache: %+v, calls %d, error %v", second, calls, err)
	}
	now = now.Add(2 * time.Minute)
	refreshFails = true
	third, err := q.Read(context.Background())
	if err != nil || len(third.Accounts) != 1 || third.Accounts[0].Cause != "sign_in_expired" || calls != 2 {
		t.Fatalf("refresh failure: %+v, calls %d, error %v", third, calls, err)
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
