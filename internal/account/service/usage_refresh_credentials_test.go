package service

import (
	"context"
	"dyna-agent/internal/account/service/adapters"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type usageRefreshTransport func(*http.Request) (*http.Response, error)

func (f usageRefreshTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestExplicitUsageRefreshRenewsCredentialsBeforeProbe(t *testing.T) {
	for _, network := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "network error"}[network], func(t *testing.T) {
			accounts, bundle, account, st := privateClaudeAccount(t)
			writeClaudeFixtureTokens(t, account.Home, "expired-access", "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", "fixture@example.test", time.Now().Add(-time.Hour))
			if _, err := accounts.Capture(context.Background(), account.ID, bundle.Executable); err != nil {
				t.Fatal(err)
			}
			old := http.DefaultTransport
			refreshes, probes := 0, 0
			http.DefaultTransport = usageRefreshTransport(func(r *http.Request) (*http.Response, error) {
				body := ""
				switch r.URL.Host {
				case "platform.claude.com":
					refreshes++
					if network {
						return nil, errors.New("private-network-error")
					}
					raw, _ := json.Marshal(map[string]any{"access_token": "renewed-access", "refresh_token": "renewed-grant", "expires_in": 3600})
					body = string(raw)
				case "api.anthropic.com":
					probes++
					if r.Header.Get("Authorization") != "Bearer renewed-access" {
						t.Fatal("usage used expired token")
					}
					body = `{"five_hour":{"utilization":12,"resets_at":"2030-03-17T17:46:40Z"}}`
				default:
					t.Fatal("unexpected provider host")
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
			})
			defer func() { http.DefaultTransport = old }()
			s := New(Options{Store: st})
			rows, _, err := s.nativeHomeAccounts(context.Background(), false)
			if err != nil || len(rows) != 1 || refreshes != 0 || probes != 0 {
				t.Fatal("plain usage read refreshed credentials or probed", err)
			}
			rows, _, err = s.nativeHomeAccounts(context.Background(), true)
			if err != nil || len(rows) != 1 {
				t.Fatal("usage refresh failed", err)
			}
			if refreshes != 1 {
				t.Fatalf("refresh calls=%d", refreshes)
			}
			if network {
				if probes != 0 || rows[0].UsageError == "" || rows[0].NeedsLogin || rows[0].Status == "needs_auth" {
					t.Fatal("network refresh failure requested sign-in or probed stale token")
				}
			} else if probes != 1 || rows[0].UsageError != "" || rows[0].NeedsLogin || len(rows[0].Windows) != 1 {
				t.Fatal("renewed usage unavailable", rows[0].UsageError)
			}
		})
	}
}

func TestCredentialRenewalAllowsModelReads(t *testing.T) {
	accounts, bundle, account, st := privateClaudeAccount(t)
	now := time.Now()
	writeClaudeFixtureTokens(t, account.Home, "expired-access", "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", "fixture@example.test", now.Add(-time.Hour))
	if _, err := accounts.Capture(t.Context(), account.ID, bundle.Executable); err != nil {
		t.Fatal(err)
	}
	s := New(Options{Store: st, Models: func(context.Context, string, string, []string) ([]adapters.Model, error) {
		return []adapters.Model{{ID: "fixture-model"}}, nil
	}})
	s.last = Snapshot{CheckedAt: now}
	reached, readable := false, false
	old := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = old })
	http.DefaultTransport = usageRefreshTransport(func(r *http.Request) (*http.Response, error) {
		reached = true
		// Fail immediately on a held lock instead of deadlocking the model read.
		if !s.mu.TryLock() {
			return nil, errors.New("credential renewal holds the model lock")
		}
		s.mu.Unlock()
		catalog, err := s.ProfileModels(r.Context(), "claude-code", true)
		readable = err == nil && len(catalog.Models) == 1 && catalog.Models[0].ID == "fixture-model"
		raw, _ := json.Marshal(map[string]any{"access_token": "renewed-access", "refresh_token": "renewed-grant", "expires_in": 3600})
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(raw))), Header: make(http.Header)}, nil
	})
	s.RefreshCredentials(t.Context(), now)
	if !reached || !readable {
		t.Fatalf("provider reached: %v, model read succeeded: %v", reached, readable)
	}
	if !s.last.CheckedAt.IsZero() {
		t.Fatal("renewed credentials left the previous usage snapshot cached")
	}
}
