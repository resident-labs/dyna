package native

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCredentialRecoverySameIdentityNewerOnly(t *testing.T) {
	for _, source := range []string{"native", "legacy"} {
		for _, kind := range []string{"newer", "older", "expired", "different-identity"} {
			t.Run(source+"/"+kind, func(t *testing.T) {
				m := claudeTestManager(t, nil)
				old := Captured{Identity: "anthropic:" + claudeTestAccountA, Credential: claudeTestPayload(t, m, claudeTestAccountA, "old", -2*time.Hour)}
				remaining := time.Hour
				id := claudeTestAccountA
				switch kind {
				case "older":
					remaining = -3 * time.Hour
				case "expired":
					remaining = -time.Hour
				case "different-identity":
					id = claudeTestAccountB
				}
				next := claudeTestPayload(t, m, id, "new", remaining)
				if source == "native" {
					claudeTestInstall(t, m, "", next)
				} else {
					dir := filepath.Join(m.host.HomeDir(), ".local", "state", "claude-accounts", id)
					p, _, _, _, _ := claudeDecode(next)
					claudeTestWrite(t, filepath.Join(dir, ".credentials.json"), p.Credentials)
					claudeTestWrite(t, filepath.Join(dir, "oauthAccount.json"), p.OAuthAccount)
				}
				got, err := m.RecoverCredential(context.Background(), old)
				if kind == "newer" {
					if err != nil || !NewerCredential(got, old) || !CredentialExpiry(got.Credential).After(m.now()) {
						t.Fatal("newer bound credential was not recovered", err)
					}
				} else if !errors.Is(err, ErrNotSignedIn) {
					t.Fatal("unsafe recovery was accepted", err)
				}
			})
		}
	}
}

func TestClaudeActiveRefreshCheckpointsBeforeNativeWrite(t *testing.T) {
	for _, failCheckpoint := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "checkpoint-failure"}[failCheckpoint], func(t *testing.T) {
			m := claudeTestManager(t, nil)
			old := claudeTestPayload(t, m, claudeTestAccountA, "old", -time.Hour)
			paths := claudeTestInstall(t, m, "", old)
			c := claudeTestCapture(t, m, "")
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("User-Agent") != claudeUserAgent {
					t.Error("missing User-Agent")
				}
				if _, err := os.Stat(filepath.Join(paths.secretDir, ".oauth_refresh.lock")); err != nil {
					t.Error("refresh lock was not held")
				}
				json.NewEncoder(w).Encode(map[string]any{"access_token": "rotated-access", "refresh_token": "rotated-refresh", "expires_in": 3600})
			}))
			defer server.Close()
			m.client = server.Client()
			m.tokenURL = server.URL
			sentinel := errors.New("checkpoint failed")
			calls := 0
			got, err := m.RefreshActive(context.Background(), c, func(next Captured) error {
				calls++
				active := claudeTestCapture(t, m, "")
				if NewerCredential(active, c) || !NewerCredential(next, c) {
					t.Error("checkpoint order is incorrect")
				}
				if failCheckpoint {
					return sentinel
				}
				return nil
			})
			if calls != 1 || !NewerCredential(got, c) {
				t.Fatal("rotation was lost")
			}
			if failCheckpoint {
				if !errors.Is(err, sentinel) {
					t.Fatal("checkpoint error was lost", err)
				}
			} else {
				if err != nil || !NewerCredential(claudeTestCapture(t, m, ""), c) {
					t.Fatal("active rotation was not written", err)
				}
			}
		})
	}
}

func TestCredentialMetadataRejectsForgedExpiryAndIdentity(t *testing.T) {
	m := claudeTestManager(t, nil)
	old := Captured{Identity: "anthropic:" + claudeTestAccountA, Credential: claudeTestPayload(t, m, claudeTestAccountA, "old", time.Hour)}
	next := old
	next.ExpiresAt = m.now().Add(24 * time.Hour)
	if NewerCredential(next, old) {
		t.Fatal("cached expiry was trusted")
	}
	next.Credential = claudeTestPayload(t, m, claudeTestAccountB, "new", 2*time.Hour)
	if NewerCredential(next, old) {
		t.Fatal("payload identity was not checked")
	}
	if _, err := m.CredentialExpiry(next); !errors.Is(err, ErrNoIdentity) {
		t.Fatal("metadata accepted mismatched identity")
	}
}

func TestActiveRefreshUsesCheckpointAfterNativeWriteFailure(t *testing.T) {
	m := claudeTestManager(t, nil)
	old := claudeTestPayload(t, m, claudeTestAccountA, "old", -time.Hour)
	claudeTestInstall(t, m, "", old)
	c := claudeTestCapture(t, m, "")
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		json.NewEncoder(w).Encode(map[string]any{"access_token": "rotated-access", "refresh_token": "rotated-refresh", "expires_in": 3600})
	}))
	defer server.Close()
	m.client = server.Client()
	m.tokenURL = server.URL
	m.write = func(string, map[string]json.RawMessage) error { return errors.New("write failed") }
	var saved Captured
	got, err := m.RefreshActive(context.Background(), c, func(next Captured) error { saved = next; return nil })
	if err == nil || !NewerCredential(saved, c) || !NewerCredential(got, c) {
		t.Fatal("failed native write lost checkpoint")
	}
	m.write = nil
	got, err = m.RefreshActive(context.Background(), saved, func(next Captured) error { return nil })
	if err != nil || calls != 1 || !NewerCredential(got, c) || !NewerCredential(claudeTestCapture(t, m, ""), c) {
		t.Fatal("retry did not restore checkpoint without stale grant refresh", err)
	}
}

func TestCredentialRecoveryCancellation(t *testing.T) {
	m := claudeTestManager(t, nil)
	c := Captured{Identity: "anthropic:" + claudeTestAccountA, Credential: claudeTestPayload(t, m, claudeTestAccountA, "old", -time.Hour)}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.RecoverCredential(ctx, c); !errors.Is(err, context.Canceled) {
		t.Fatal("recovery ignored cancellation", err)
	}
}

func TestActiveRefreshRejectsChangedNativeAccount(t *testing.T) {
	m := claudeTestManager(t, nil)
	c := Captured{Identity: "anthropic:" + claudeTestAccountA, Credential: claudeTestPayload(t, m, claudeTestAccountA, "old", -time.Hour)}
	claudeTestInstall(t, m, "", claudeTestPayload(t, m, claudeTestAccountB, "other", time.Hour))
	calls := 0
	if _, err := m.RefreshActive(context.Background(), c, func(Captured) error { calls++; return nil }); !errors.Is(err, ErrNoIdentity) || calls != 0 {
		t.Fatal("changed active identity was accepted", err)
	}
}

type credentialSupportFixture struct {
	Manager
	caps      Caps
	supported bool
}

func (m credentialSupportFixture) Caps() Caps                              { return m.caps }
func (m credentialSupportFixture) SupportsCredentialRefresh(Captured) bool { return m.supported }

func TestCredentialRefreshSupportIsSharedAndConservative(t *testing.T) {
	for _, tc := range []struct{ operation, credential, want bool }{{false, false, false}, {false, true, false}, {true, false, false}, {true, true, true}} {
		manager := credentialSupportFixture{caps: Caps{Refresh: tc.operation}, supported: tc.credential}
		if got := SupportsRefresh(manager, Captured{}); got != tc.want {
			t.Fatalf("operation=%v credential=%v: %v", tc.operation, tc.credential, got)
		}
	}
	// Existing adapters without a credential-specific gate retain their
	// capability.
	for _, harness := range []string{"claude-code"} {
		manager, ok := For(harness, Host{})
		if !ok {
			t.Fatal(harness)
		}
		if SupportsRefresh(manager, Captured{}) != manager.Caps().Refresh {
			t.Fatal("existing capability changed", harness)
		}
	}
}
