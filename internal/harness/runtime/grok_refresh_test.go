package runtime

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/account/native"
	"dyna-agent/internal/account/store"
)

type grokRefreshTransport func(*http.Request) (*http.Response, error)

func (f grokRefreshTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestGrokCredentialRefreshUsesTransactionAndRuntime(t *testing.T) {
	for _, tc := range []struct {
		name    string
		body    string
		network bool
		invalid bool
	}{
		{"rotation", `{"access_token":"new-access","refresh_token":"rotated-grant","expires_in":3600}`, false, false},
		{"invalid grant", `{"error":"invalid_grant"}`, false, true},
		{"network", "", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := testAccounts(t)
			n, err := a.Create("grok", "")
			if err != nil {
				t.Fatal(err)
			}
			auth := strings.Replace(grokOIDCAuth, `"key":"fixture-session"`, `"key":"fixture-session","refresh_token":"old-grant","expires_at":"2026-01-01T00:00:00Z"`, 1)
			if err = os.WriteFile(filepath.Join(n.Home, "auth.json"), []byte(auth), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err = a.Capture(context.Background(), n.ID, "/fake/grok"); err != nil {
				t.Fatal(err)
			}
			if err = a.Switch(context.Background(), n.ID, "/fake/grok"); err != nil {
				t.Fatal(err)
			}
			home, _ := a.HarnessHome("grok")
			before, _ := os.ReadFile(filepath.Join(home, "auth.json"))
			savedBefore, _ := os.ReadFile(filepath.Join(n.Home, ".login.json"))
			transport := http.DefaultTransport
			calls := 0
			http.DefaultTransport = grokRefreshTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.String() != "https://auth.x.ai/oauth2/token" {
					t.Fatal("wrong refresh URL")
				}
				if tc.network {
					return nil, errors.New("private-network-error")
				}
				status := 200
				if tc.invalid {
					status = 400
				}
				return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(tc.body)), Header: make(http.Header)}, nil
			})
			defer func() { http.DefaultTransport = transport }()
			result, err := a.RefreshCredentials(context.Background(), n.ID, "/fake/grok")
			if calls != 1 {
				t.Fatalf("refresh did not reach provider: %d, %v", calls, err)
			}
			after, _ := os.ReadFile(filepath.Join(home, "auth.json"))
			savedAfter, _ := os.ReadFile(filepath.Join(n.Home, ".login.json"))
			st, readErr := (&store.Store{Root: a.Root}).Read()
			if readErr != nil {
				t.Fatal(readErr)
			}
			if tc.network || tc.invalid {
				if err == nil || errors.Is(err, native.ErrInvalidGrant) != tc.invalid || string(after) != string(before) || string(savedAfter) != string(savedBefore) || st.Find(n.ID).NeedsLogin != tc.invalid {
					t.Fatal("refresh failure changed credentials or sign-in state", err)
				}
				return
			}
			if err != nil || !result.Refreshed || !strings.Contains(string(after), "rotated-grant") || !strings.Contains(string(savedAfter), "rotated-grant") || st.Find(n.ID).NeedsLogin {
				t.Fatal("rotation not saved to both locations", err)
			}
			login, err := readAccountLogin(n.Home)
			if err != nil || time.Until(native.CredentialExpiry(login.Credential)) < 59*time.Minute {
				t.Fatal("snapshot expiry missing", err)
			}
		})
	}
}

func TestGrokRefreshRetriesCheckpointWithoutOldExpiry(t *testing.T) {
	a := testAccounts(t)
	n, err := a.Create("grok", "")
	if err != nil {
		t.Fatal(err)
	}
	auth := strings.Replace(grokOIDCAuth, `"key":"fixture-session"`, `"key":"fixture-session","refresh_token":"old-grant"`, 1)
	if err = os.WriteFile(filepath.Join(n.Home, "auth.json"), []byte(auth), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = a.Capture(context.Background(), n.ID, "/fake/grok"); err != nil {
		t.Fatal(err)
	}
	if err = a.Switch(context.Background(), n.ID, "/fake/grok"); err != nil {
		t.Fatal(err)
	}
	home, _ := a.HarnessHome("grok")
	path := filepath.Join(home, "auth.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	transport := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = transport })
	calls := 0
	http.DefaultTransport = grokRefreshTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if calls > 1 {
			t.Error("retry contacted the provider instead of installing the saved rotation")
			return nil, errors.New("unexpected provider retry")
		}
		// Make native activation fail after the shared transaction checkpoints.
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"access_token":"new-access","refresh_token":"rotated-grant","expires_in":3600}`)), Header: make(http.Header)}, nil
	})
	if _, err = a.RefreshCredentials(context.Background(), n.ID, "/fake/grok"); err == nil {
		t.Fatal("native activation unexpectedly succeeded")
	}
	saved, err := readAccountLogin(n.Home)
	if err != nil || !strings.Contains(string(saved.Credential.Payload), "rotated-grant") {
		t.Fatal("failed activation lost the checkpoint", err)
	}
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, before, 0600); err != nil {
		t.Fatal(err)
	}
	result, err := a.RefreshCredentials(context.Background(), n.ID, "/fake/grok")
	if err != nil || !result.Refreshed || calls != 1 {
		t.Fatal("checkpoint recovery failed", result, err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(after), "rotated-grant") {
		t.Fatal("rotation did not reach native storage", err)
	}
}
