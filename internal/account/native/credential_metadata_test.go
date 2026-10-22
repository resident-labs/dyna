package native

import (
	"context"
	"dyna-agent/internal/account/store"
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

func TestCodexActiveRefreshCheckpointsNativeRotation(t *testing.T) {
	m, root := codexTestManager(t)
	raw := codexTestAuth("person", "workspace", "old", time.Now().Add(-time.Hour))
	codexTestWrite(t, filepath.Join(root, "auth.json"), raw)
	c, _ := codexParse(raw)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"access_token": codexTestJWT(map[string]int64{"exp": time.Now().Add(time.Hour).Unix()}), "refresh_token": "rotated"})
	}))
	defer server.Close()
	m.client = server.Client()
	m.tokenURL = server.URL
	calls := 0
	got, err := m.RefreshActive(context.Background(), c, func(next Captured) error {
		calls++
		rows, e := m.Capture(context.Background(), "")
		if e != nil || len(rows) != 1 || NewerCredential(rows[0], c) || !NewerCredential(next, c) {
			t.Error("wrong checkpoint order")
		}
		return nil
	})
	rows, e := m.Capture(context.Background(), "")
	if err != nil || e != nil || calls != 1 || len(rows) != 1 || !NewerCredential(rows[0], c) || !NewerCredential(got, c) {
		t.Fatal("active Codex rotation failed", err, e)
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

func TestCredentialExpiryReportsMissingMetadata(t *testing.T) {
	meta := json.RawMessage(`{"mechanism":"oauth","access_token":"fake-access","refresh_token":"fake-refresh","user_email":"fixture@example.invalid"}`)
	rows, err := museCaptureMeta(meta)
	if err != nil {
		t.Fatal(err)
	}
	m := &museManager{}
	if _, err = m.CredentialExpiry(rows[0]); !errors.Is(err, ErrUnsupported) {
		t.Fatal("missing expiry was not unsupported", err)
	}
	rows[0].ExpiresAt = time.Now().Add(time.Hour)
	if !CredentialExpiry(rows[0].Credential).IsZero() {
		t.Fatal("cached expiry was trusted")
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

func TestCodexActiveRefreshRestoresNewerSavedCredential(t *testing.T) {
	m, root := codexTestManager(t)
	raw := codexTestAuth("person", "workspace", "old", time.Now().Add(-time.Hour))
	codexTestWrite(t, filepath.Join(root, "auth.json"), raw)
	old, _ := codexParse(raw)
	saved, _ := codexParse(codexTestAuth("person", "workspace", "rotated", time.Now().Add(time.Hour)))
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("newer saved grant should not refresh") }))
	defer server.Close()
	m.client = server.Client()
	m.tokenURL = server.URL
	calls := 0
	got, err := m.RefreshActive(context.Background(), saved, func(next Captured) error {
		calls++
		if !NewerCredential(next, old) {
			t.Error("stale native grant was used")
		}
		return nil
	})
	rows, e := m.Capture(context.Background(), "")
	if err != nil || e != nil || calls != 1 || len(rows) != 1 || !NewerCredential(rows[0], old) || !NewerCredential(got, old) {
		t.Fatal("saved rotation was not restored", err, e)
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

// A legacy account's CLI token lives only in its saved payload: active refresh
// checkpoints the refreshed payload and writes no native file.
func TestAntigravityActiveRefreshCheckpointsTheSavedPayloadOnly(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	c := directTestAgy(t, now.Add(-time.Hour))
	m := agyTestManager(t)
	m.now = func() time.Time { return now }
	m.clientSecret = func(context.Context) (string, error) { return "fake-secret", nil }
	m.client = directTestClient(func(r *http.Request) (*http.Response, error) {
		return directTestResponse(200, `{"access_token":"new-access","refresh_token":"rotated","expires_in":3600}`), nil
	})
	checkpoints := 0
	got, err := m.RefreshActive(context.Background(), c, func(next Captured) error {
		checkpoints++
		if !NewerCredential(next, c) {
			t.Error("checkpoint is not the refreshed payload")
		}
		return nil
	})
	if err != nil || checkpoints != 1 || !NewerCredential(got, c) {
		t.Fatal("active Antigravity refresh failed", err)
	}
	if entries, _ := os.ReadDir(m.host.Home); len(entries) != 0 {
		t.Fatal("active refresh wrote native files")
	}
	// An ACP-only account has nothing to refresh.
	saved := agyACPTestSaved(t, "acp")
	if next, err := m.RefreshActive(context.Background(), saved, func(Captured) error { t.Error("checkpointed an ACP-only account"); return nil }); err != nil || next.Identity != saved.Identity {
		t.Fatal(err)
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
	// Existing adapters without a credential-specific gate retain their capability.
	for _, harness := range []string{"claude-code", "codex", "antigravity", "muse", "opencode"} {
		manager, ok := For(harness, Host{})
		if !ok {
			t.Fatal(harness)
		}
		if SupportsRefresh(manager, Captured{}) != manager.Caps().Refresh {
			t.Fatal("existing capability changed", harness)
		}
	}
}

func TestAPIKeyFallbackRequiresValidIdentity(t *testing.T) {
	payload := json.RawMessage(`{"metaCredential":{"api_key":"fixture-key","access_token":"fixture-access","user_email":"fixture@example.test","expires_at":1}}`)
	c := Captured{Identity: "meta:email:fixture@example.test", Credential: store.Credential{Schema: museAuthSchema, Payload: payload}}
	if !CredentialUsesAPIKey(c) || CredentialExpired(c, time.Now()) {
		t.Fatal("valid key fallback rejected")
	}
	c.Identity = "meta:email:another@example.test"
	if CredentialUsesAPIKey(c) || !CredentialExpired(c, time.Now()) {
		t.Fatal("mismatched key accepted")
	}
	c.Credential.Payload = json.RawMessage(`{`)
	if CredentialUsesAPIKey(c) {
		t.Fatal("malformed key accepted")
	}
}
