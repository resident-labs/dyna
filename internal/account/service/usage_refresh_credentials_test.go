package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type usageRefreshTransport func(*http.Request) (*http.Response, error)

func (f usageRefreshTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestExplicitUsageRefreshRenewsCredentialsBeforeProbe(t *testing.T) {
	for _, network := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "network error"}[network], func(t *testing.T) {
			accounts, bundle, account, st := privateCodexAccount(t)
			home, _ := accounts.HarnessHome("codex")
			jwt := func(expiry time.Time) string {
				b, _ := json.Marshal(map[string]int64{"exp": expiry.Unix()})
				return "header." + base64.RawURLEncoding.EncodeToString(b) + ".signature"
			}
			path := filepath.Join(home, "auth.json")
			b, _ := os.ReadFile(path)
			b = []byte(strings.Replace(string(b), "fixture-access", jwt(time.Now().Add(-time.Hour)), 1))
			if err := os.WriteFile(path, b, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := accounts.Capture(context.Background(), account.ID, bundle.Executable); err != nil {
				t.Fatal(err)
			}
			old := http.DefaultTransport
			refreshes, probes := 0, 0
			access := jwt(time.Now().Add(time.Hour))
			http.DefaultTransport = usageRefreshTransport(func(r *http.Request) (*http.Response, error) {
				body := ""
				switch r.URL.Host {
				case "auth.openai.com":
					refreshes++
					if network {
						return nil, errors.New("private-network-error")
					}
					raw, _ := json.Marshal(map[string]string{"access_token": access, "refresh_token": "renewed-grant"})
					body = string(raw)
				case "chatgpt.com":
					probes++
					if r.Header.Get("Authorization") != "Bearer "+access {
						t.Fatal("usage used expired token")
					}
					body = `{"rate_limit":{"primary_window":{"used_percent":12,"limit_window_seconds":604800,"reset_at":1900000000}}}`
				default:
					t.Fatal("unexpected provider host")
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
			})
			defer func() { http.DefaultTransport = old }()
			s := New(Options{Store: st})
			rows, err := s.nativeHomeAccounts(context.Background(), false)
			if err != nil || len(rows) != 1 || refreshes != 0 || probes != 0 {
				t.Fatal("plain usage read refreshed credentials or probed", err)
			}
			rows, err = s.nativeHomeAccounts(context.Background(), true)
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
