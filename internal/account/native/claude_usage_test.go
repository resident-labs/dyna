package native

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestFetchClaudeUsageSendsReadOnlyRequestWithNativeToken(t *testing.T) {
	m := claudeTestManager(t, nil)
	c := claudeTestPayload(t, m, claudeTestAccountA, "a", time.Hour)
	p := claudeTestInstall(t, m, "", c)
	before, _ := os.ReadFile(p.credentials)
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("User-Agent") != claudeUserAgent || r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer fixture-access-a" || r.Header.Get("anthropic-beta") != "oauth-2025-04-20" {
			t.Error("unexpected usage request")
		}
		w.Write([]byte(`{"five_hour":{"utilization":10}}`))
	}))
	defer srv.Close()
	got, err := FetchClaudeUsage(context.Background(), m.host, ClaudeUsageOptions{Client: srv.Client(), URL: srv.URL, Now: m.now})
	if err != nil || calls != 1 || got.Identity != "anthropic:"+claudeTestAccountA || !strings.Contains(string(got.Body), "five_hour") {
		t.Fatalf("usage fetch failed: %v", err)
	}
	after, _ := os.ReadFile(p.credentials)
	if string(before) != string(after) {
		t.Fatal("usage fetch changed native credentials")
	}
}

func TestFetchClaudeUsageNeverRefreshesAnExpiringToken(t *testing.T) {
	m := claudeTestManager(t, nil)
	claudeTestInstall(t, m, "", claudeTestPayload(t, m, claudeTestAccountA, "a", 30*time.Second))
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("no request may be sent") }))
	defer srv.Close()
	_, err := FetchClaudeUsage(context.Background(), m.host, ClaudeUsageOptions{Client: srv.Client(), URL: srv.URL, Now: m.now})
	if !errors.Is(err, ErrInvalidGrant) {
		t.Fatal("an expiring token must report unknown usage")
	}
}

func TestFetchClaudeUsageDiscardsResponseWhenAccountChanges(t *testing.T) {
	m := claudeTestManager(t, nil)
	claudeTestInstall(t, m, "", claudeTestPayload(t, m, claudeTestAccountA, "a", time.Hour))
	other := claudeTestPayload(t, m, claudeTestAccountB, "b", time.Hour)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claudeTestInstall(t, m, "", other)
		w.Write([]byte(`{"five_hour":{"utilization":10}}`))
	}))
	defer srv.Close()
	if _, err := FetchClaudeUsage(context.Background(), m.host, ClaudeUsageOptions{Client: srv.Client(), URL: srv.URL, Now: m.now}); err == nil {
		t.Fatal("usage from a switched account must be discarded")
	}
}

func TestFetchClaudeUsageErrorsHideResponseBody(t *testing.T) {
	m := claudeTestManager(t, nil)
	claudeTestInstall(t, m, "", claudeTestPayload(t, m, claudeTestAccountA, "a", time.Hour))
	for _, code := range []int{http.StatusTooManyRequests, http.StatusUnauthorized} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(code)
			w.Write([]byte("fixture-private-body"))
		}))
		_, err := FetchClaudeUsage(context.Background(), m.host, ClaudeUsageOptions{Client: srv.Client(), URL: srv.URL, Now: m.now})
		srv.Close()
		if err == nil || strings.Contains(err.Error(), "fixture-private") || strings.Contains(err.Error(), "fixture-access") {
			t.Fatal("usage errors must not expose response or token data")
		}
	}
}
