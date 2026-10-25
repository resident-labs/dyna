package service

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/account/native"
	"dyna-agent/internal/account/store"
	"dyna-agent/internal/harness/runtime"
)

func TestUnsupportedCredentialExpiryOffersSignIn(t *testing.T) {
	for _, harness := range []string{"muse", "opencode"} {
		t.Run(harness, func(t *testing.T) {
			for _, expiry := range []time.Time{time.Now().Add(-time.Hour), time.Now().Add(time.Hour), {}} {
				a := Account{Harness: harness, Status: "ready"}
				setCredentialExpiry(&a, native.Captured{ExpiresAt: expiry})
				expired := !expiry.IsZero() && expiry.Before(time.Now())
				if a.NeedsLogin != expired || (a.Status == "needs_auth") != expired {
					t.Fatalf("expiry=%v: %+v", expiry, a)
				}
				if a.RefreshMessage == "" {
					t.Fatal("missing sign-in guidance")
				}
				if expiry.IsZero() != (a.ExpiresAt == nil) {
					t.Fatal("invented or lost expiry")
				}
			}
		})
	}
	a := Account{Harness: "codex", Status: "ready"}
	setCredentialExpiry(&a, native.Captured{ExpiresAt: time.Now().Add(-time.Hour)})
	if a.NeedsLogin {
		t.Fatal("refreshable token incorrectly required sign-in")
	}
}

func TestExpiredAccessTokenKeepsAPIKeyFallbackSignedIn(t *testing.T) {
	expiry := time.Now().Add(-time.Hour)
	for _, h := range []string{"muse"} {
		payload, _ := json.Marshal(map[string]any{"metaCredential": map[string]any{"access_token": "fixture-token", "api_key": "fixture-key", "user_email": "fixture@example.test", "expires_at": expiry.Unix()}})
		schema, identity := "muse/auth-file@1", "meta:email:fixture@example.test"
		a := Account{Harness: h, Status: "ready"}
		setCredentialExpiry(&a, native.Captured{Identity: identity, Credential: store.Credential{Schema: schema, Payload: payload}})
		if strings.Contains(a.RefreshMessage, "Sign in again before") || a.RefreshMessage != "API key · no expiry" {
			t.Fatalf("misleading fallback guidance for %s: %s", h, a.RefreshMessage)
		}
		if a.NeedsLogin || a.Status != "ready" || a.ExpiresAt == nil {
			t.Fatalf("fallback lost for %s: %+v", h, a)
		}
	}
}

func TestPrivateNativeUsageAuthenticationFailureOffersSignIn(t *testing.T) {
	oldPlan := &Plan{ID: "old"}
	for _, mode := range []string{"rejected", "temporary", "success"} {
		t.Run(mode, func(t *testing.T) {
			a := Account{Harness: "grok", Status: "ready", Plan: oldPlan, Windows: []Window{{ID: "old-quota"}}}
			observation := store.Observation{Windows: []Window{{ID: "grok-weekly"}}, ObservedAt: time.Now()}
			var err error
			if mode == "rejected" {
				err = native.ErrInvalidGrant
			}
			if mode == "temporary" {
				err = errors.New("temporary provider failure")
			}
			applyNativeUsageObservation(&a, observation, err)
			if mode == "rejected" && (a.Status != "needs_auth" || !a.NeedsLogin || a.Plan != nil || len(a.Windows) != 0) {
				t.Fatal("rejected Grok login retained quota or hid Sign in")
			}
			if mode == "temporary" && (a.Status != "ready" || a.NeedsLogin || a.Plan != oldPlan || len(a.Windows) != 1) {
				t.Fatal("temporary failure discarded saved usage")
			}
			if mode == "success" && (len(a.Windows) != 1 || a.Windows[0].ID != "grok-weekly" || !a.ObservedAt.Equal(observation.ObservedAt)) {
				t.Fatal("verified weekly observation was not applied")
			}
		})
	}
}

func TestSuccessfulUsageRefreshClearsSavedStaleFlag(t *testing.T) {
	now := time.Date(2026, 10, 18, 20, 0, 0, 0, time.UTC)
	oldUsed, newUsed := 21.0, 27.0
	for _, failure := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "provider_failure"}[failure], func(t *testing.T) {
			account := Account{Harness: "claude-code", Active: true, Status: "ready"}
			applySavedObservation(&account, store.Entry{Metadata: store.Metadata{Harness: account.Harness, Observation: &store.Observation{
				ObservedAt: now.Add(-time.Hour), Windows: []Window{{ID: "session", UsedPercent: &oldUsed}},
			}}}, now)
			if !account.Stale {
				t.Fatal("fixture must start with stale saved usage")
			}
			var probeErr error
			if failure {
				probeErr = errors.New("provider unavailable")
			}
			applyNativeUsageObservation(&account, store.Observation{ObservedAt: now, Windows: []Window{{ID: "session", UsedPercent: &newUsed}}}, probeErr)
			if failure {
				if !account.Stale || !account.ObservedAt.Equal(now.Add(-time.Hour)) || *account.Windows[0].UsedPercent != oldUsed {
					t.Fatalf("failed refresh changed saved usage: %+v", account)
				}
			} else if account.Stale || !account.ObservedAt.Equal(now) || *account.Windows[0].UsedPercent != newUsed {
				t.Fatalf("successful refresh must replace stale saved usage: %+v", account)
			}
			service := New(Options{Now: func() time.Time { return now }})
			service.last = Snapshot{Accounts: []Account{account}}
			if cached := service.AccountUsageCached().Accounts[0]; cached.Stale != failure {
				t.Fatalf("cached refresh result stale = %v, want %v", cached.Stale, failure)
			}
		})
	}
}

func TestCurrentCredentialClearsSavedRefreshWarning(t *testing.T) {
	a := Account{Harness: "grok", Status: "ready", RefreshMessage: "This login cannot be refreshed automatically."}
	expiry := time.Now().Add(time.Hour).UTC()
	auth, _ := json.Marshal(map[string]any{"https://auth.x.ai::b1a00492-073a-47ea-816f-4c329264a828": map[string]any{"auth_mode": "oidc", "key": "fixture-access", "refresh_token": "fixture-refresh", "user_id": "fixture-user", "oidc_issuer": "https://auth.x.ai", "oidc_client_id": "b1a00492-073a-47ea-816f-4c329264a828", "expires_at": expiry.Format(time.RFC3339Nano)}})
	payload, _ := json.Marshal(map[string]json.RawMessage{"auth.json": auth})
	setCredentialExpiry(&a, native.Captured{Identity: "grok:native", Credential: store.Credential{Schema: "grok/native-files@1", Payload: payload}})
	if a.RefreshMessage != "" || a.ExpiresAt == nil || !a.ExpiresAt.Equal(expiry) {
		t.Fatal("saved fallback warning hid current refresh support", a.RefreshMessage)
	}
}

func TestMuseAPIKeyHasNoExpiryWarning(t *testing.T) {
	payload := json.RawMessage(`{"metaCredential":{"api_key":"fixture-key","access_token":"fixture-access","user_email":"fixture@example.test"}}`)
	a := Account{Harness: "muse", Status: "ready"}
	setCredentialExpiry(&a, native.Captured{Identity: "meta:email:fixture@example.test", Credential: store.Credential{Schema: "muse/auth-file@1", Payload: payload}})
	if a.RefreshMessage != "API key · no expiry" || a.NeedsLogin {
		t.Fatalf("static key status: %+v", a)
	}
}

func TestSavedUsagePreservesPrivateCredentialStatus(t *testing.T) {
	expiry := time.Now().Add(time.Hour)
	a := Account{Harness: "muse", RefreshMessage: "API key · no expiry", ExpiresAt: &expiry}
	applySavedObservation(&a, store.Entry{Metadata: store.Metadata{Harness: "muse", NativeHome: t.TempDir()}}, time.Now())
	if a.RefreshMessage != "API key · no expiry" || a.ExpiresAt == nil {
		t.Fatal("index without credentials overwrote native login status", a)
	}
}

func TestOpenCodeSavedReferenceExpiryIsInformational(t *testing.T) {
	c := native.Captured{Identity: "opencode:opencode-go:fixture", Integration: "opencode-go", Credential: store.Credential{Schema: "opencode/v2-ref@1", Payload: json.RawMessage(`{"ref":{"integration":"opencode-go","credentialID":"fixture","scope":{},"credentialType":"oauth","expires":1}}`)}}
	a := Account{Harness: "opencode", Status: "ready"}
	setCredentialExpiry(&a, c)
	if a.NeedsLogin || a.Status != "ready" {
		t.Fatal("reference expiry rejected native login", a)
	}
	c.Credential.Payload = json.RawMessage(`{"ref":{"integration":"opencode-go","credentialID":"fixture","scope":{},"credentialType":"oauth","expires":1999999999999}}`)
	setCredentialExpiry(&a, c)
	if a.NeedsLogin || a.Status != "ready" || a.ExpiresAt.UnixMilli() != 1999999999999 {
		t.Fatal("current native expiry lost", a)
	}
}

// A saved login of a retired harness keeps reporting usage and renewing: the
// bundle and executable a turn would need are no longer required.
func TestRetiredHarnessAccountRefreshesAndReportsUsageWithoutBundle(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	root := filepath.Join(t.TempDir(), "accounts")
	accounts := &runtime.Accounts{Root: root}
	n, err := accounts.Create("grok", "saved")
	if err != nil {
		t.Fatal(err)
	}
	const client = "b1a00492-073a-47ea-816f-4c329264a828"
	auth, err := json.Marshal(map[string]any{"https://auth.x.ai::" + client: map[string]any{
		"auth_mode": "oidc", "key": "fixture-access", "refresh_token": "fixture-refresh", "user_id": "fixture-user",
		"oidc_issuer": "https://auth.x.ai", "oidc_client_id": client, "expires_at": now.Add(time.Hour).UTC().Format(time.RFC3339Nano),
	}})
	if err != nil {
		t.Fatal(err)
	}
	home, err := accounts.HarnessHome("grok")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	// The account slot holds the saved login; the shared home holds the login
	// the usage reader captures.
	for _, dir := range []string{home, n.Home} {
		if err := os.WriteFile(filepath.Join(dir, "auth.json"), auth, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := accounts.Capture(t.Context(), n.ID, ""); err != nil {
		t.Fatal(err)
	}
	// A signed-in account; activation would clear this flag.
	if err := (&store.Store{Root: root}).Update(func(state *store.State) error {
		state.Find(n.ID).NeedsLogin = false
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := (&store.Store{Root: root}).SetActive(store.ProviderXAI, "", n.ID); err != nil {
		t.Fatal(err)
	}
	s := New(Options{Store: &store.Store{Root: root}, Now: func() time.Time { return now }})
	rows, err := s.nativeHomeAccounts(t.Context(), false)
	if err != nil || len(rows) != 1 || rows[0].Status != "ready" {
		t.Fatalf("retired harness account = %+v, %v", rows, err)
	}
	if runtime.RunsAsHarness("grok") || runtime.RunsAsHarness("kimi-code") {
		t.Fatal("a retired harness is still runnable")
	}
	// Renewal reaches the saved login: the next check is its refresh time, not
	// the retry ceiling a skipped account would leave.
}
