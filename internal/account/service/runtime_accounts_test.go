package service

import (
	"encoding/json"
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
	"dyna-agent/internal/harness/runtime"
)

func TestUnsupportedCredentialExpiryOffersSignIn(t *testing.T) {
	for _, harness := range []string{"pi", "kimi-code"} {
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
	// A grok sign-in that can refresh needs no sign-in for a past expiry.
	expired := time.Now().Add(-time.Hour)
	payload, err := json.Marshal(map[string]any{"auth.json": map[string]any{"https://auth.x.ai::b1a00492-073a-47ea-816f-4c329264a828": map[string]any{
		"auth_mode": "oidc", "oidc_issuer": "https://auth.x.ai", "oidc_client_id": "b1a00492-073a-47ea-816f-4c329264a828",
		"user_id": "fixture-user", "key": "fixture-access", "refresh_token": "fixture-refresh", "expires_at": expired,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	a := Account{Harness: "grok", Status: "ready"}
	setCredentialExpiry(&a, native.Captured{Identity: "grok:native", ExpiresAt: expired, Credential: store.Credential{Schema: "grok/native-files@1", Payload: payload}})
	if a.NeedsLogin {
		t.Fatal("refreshable token incorrectly required sign-in")
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

// grokAuthFile is one login in Grok's auth.json.
type grokAuthFile struct {
	AuthMode     string `json:"auth_mode"`
	Key          string `json:"key"`
	RefreshToken string `json:"refresh_token"`
	UserID       string `json:"user_id"`
	OIDCIssuer   string `json:"oidc_issuer"`
	OIDCClientID string `json:"oidc_client_id"`
	ExpiresAt    string `json:"expires_at"`
}

// grokAuthFiles is Grok's auth.json, keyed by issuer and client.
type grokAuthFiles map[string]grokAuthFile

func TestRetiredHarnessAccountRefreshesAndReportsUsageWithoutBundle(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	root := filepath.Join(t.TempDir(), "accounts")
	accounts := &runtime.Accounts{Root: root}
	n, err := accounts.Create("grok", "saved")
	if err != nil {
		t.Fatal(err)
	}
	const client = "b1a00492-073a-47ea-816f-4c329264a828"
	auth, err := json.Marshal(grokAuthFiles{"https://auth.x.ai::" + client: {
		AuthMode: "oidc", Key: "fixture-access", RefreshToken: "fixture-refresh", UserID: "fixture-user",
		OIDCIssuer: "https://auth.x.ai", OIDCClientID: client, ExpiresAt: now.Add(time.Hour).UTC().Format(time.RFC3339Nano),
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
	if err := (&store.Store{Root: root}).SetActive(store.ProviderXAI, "grok", "", n.ID); err != nil {
		t.Fatal(err)
	}
	s := New(Options{Store: &store.Store{Root: root}, Now: func() time.Time { return now }})
	rows, _, err := s.nativeHomeAccounts(t.Context(), false)
	if err != nil || len(rows) != 1 || rows[0].Status != "ready" {
		t.Fatalf("retired harness account = %+v, %v", rows, err)
	}
	if runtime.RunsAsHarness("grok") || runtime.RunsAsHarness("kimi-code") {
		t.Fatal("a retired harness is still runnable")
	}
	// Renewal reaches the saved login: the next check is its refresh time, not
	// the retry ceiling a skipped account would leave.
}

// kimiToken is one Kimi Code credential file.
type kimiToken struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresAt    int64  `json:"expires_at"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
}

// kimiNativeFiles is the saved payload of one Kimi Code account.
type kimiNativeFiles struct {
	Config      string `json:"config.toml"`
	Credentials string `json:"credentials/kimi-code.json"`
}

const kimiConfig = `[providers."managed:kimi-code"]
type = "kimi"
base_url = "https://api.kimi.ai/coding/v1"

[providers."managed:kimi-code".oauth]
storage = "file"
key = "oauth/kimi-code"
oauthHost = "https://auth.kimi.ai"
`

const kimiUsageBody = `{"usages":{"limit_5h":{"used_ratio":0.1241,"reset_time":"2026-10-25T15:22:12Z"},"limit_7d":{"used_ratio":0.2,"reset_time":"2026-11-01T00:00:00Z"}}}`

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func stubResponse(body string) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

const kimiAccessToken = "fixture-access"

// kimiLogin builds the saved login for one Kimi Code identity.
func kimiLogin(identity string, expiry time.Time) native.Captured {
	rawToken, _ := json.Marshal(kimiToken{AccessToken: kimiAccessToken, RefreshToken: "fixture-refresh", ExpiresAt: expiry.Unix(), TokenType: "Bearer"})
	payload, _ := json.Marshal(kimiNativeFiles{Config: kimiConfig, Credentials: string(rawToken)})
	return native.Captured{Identity: identity, Credential: store.Credential{Schema: "kimi-code/native-files@1", Payload: payload}}
}

// writeKimiLogin writes the shared Kimi Code login the account manager reads.
func writeKimiLogin(t *testing.T, accounts *runtime.Accounts, expiry time.Time) {
	t.Helper()
	rawToken, err := json.Marshal(kimiToken{AccessToken: kimiAccessToken, RefreshToken: "fixture-refresh", ExpiresAt: expiry.Unix(), TokenType: "Bearer"})
	if err != nil {
		t.Fatal(err)
	}
	shared, err := accounts.HarnessHome("kimi-code")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(shared, "credentials"), 0700); err != nil {
		t.Fatal(err)
	}
	for path, body := range map[string][]byte{
		filepath.Join(shared, "config.toml"):                   []byte(kimiConfig),
		filepath.Join(shared, "credentials", "kimi-code.json"): rawToken,
	} {
		if err := os.WriteFile(path, body, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func writeSavedLogin(t *testing.T, home string, c native.Captured) {
	t.Helper()
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".login.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
}

// A saved Kimi Code account still reports usage and renews without a bundle:
// usage comes from the provider with the saved grant, and renewal saves the
// rotated credential. Both requests are faked.
func TestKimiRetiredAccountUsesAndRenewsWithoutBundle(t *testing.T) {
	now := time.Now().UTC()
	old := http.DefaultTransport
	http.DefaultTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/usages"):
			if got := r.Header.Get("Authorization"); got != "Bearer "+kimiAccessToken {
				t.Errorf("usage auth = %q", got)
			}
			return stubResponse(kimiUsageBody), nil
		case strings.HasSuffix(r.URL.Path, "/api/oauth/token"):
			rotated, err := json.Marshal(kimiToken{AccessToken: "rotated-access", RefreshToken: "rotated-refresh", ExpiresIn: 3600, TokenType: "Bearer"})
			if err != nil {
				return nil, err
			}
			return stubResponse(string(rotated)), nil
		}
		return nil, errors.New("unexpected provider request: " + r.URL.String())
	})
	t.Cleanup(func() { http.DefaultTransport = old })

	root := filepath.Join(t.TempDir(), "accounts")
	accounts := &runtime.Accounts{Root: root}
	n, err := accounts.Create("kimi-code", "saved")
	if err != nil {
		t.Fatal(err)
	}
	writeKimiLogin(t, accounts, now.Add(time.Hour))
	if err := (&store.Store{Root: root}).SetActive(store.ProviderKimiCode, "kimi-code", "", n.ID); err != nil {
		t.Fatal(err)
	}
	// Save the login the account reports, so renewal has a credential to rotate.
	manager, err := accounts.Manager(n.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	captured, err := manager.Capture(t.Context(), "")
	if err != nil || len(captured) != 1 {
		t.Fatalf("capture = %+v, %v", captured, err)
	}
	writeSavedLogin(t, n.Home, captured[0])
	if err := (&store.Store{Root: root}).Update(func(state *store.State) error {
		entry := state.Find(n.ID)
		entry.Identity, entry.NeedsLogin = captured[0].Identity, false
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := (&runtime.BundleManager{Root: filepath.Join(filepath.Dir(root), "harnesses")}).Resolve("kimi-code", ""); err == nil {
		t.Fatal("the fixture has a runnable Kimi bundle")
	}
	s := New(Options{Store: &store.Store{Root: root}, Now: func() time.Time { return now }})
	rows, _, err := s.nativeHomeAccounts(t.Context(), true)
	if err != nil || len(rows) != 1 {
		t.Fatalf("accounts = %+v, %v", rows, err)
	}
	if row := rows[0]; row.Status != "ready" || len(row.Windows) != 2 || row.Windows[0].ID != "five_hour" {
		t.Fatalf("kimi account = %+v", row)
	}

	// A due saved login renews and checkpoints the rotated grant.
	writeKimiLogin(t, accounts, now.Add(-time.Minute))
	writeSavedLogin(t, n.Home, kimiLogin(captured[0].Identity, now.Add(-time.Minute)))
	// The scheduled pass is what must reach a retired harness.
	if next := s.RefreshCredentials(t.Context(), now); !next.Equal(now.Add(time.Minute)) {
		t.Fatalf("next credential check = %v, want one minute after a renewal", next)
	}
	saved, err := os.ReadFile(filepath.Join(n.Home, ".login.json"))
	if err != nil {
		t.Fatal(err)
	}
	var renewed native.Captured
	if err := json.Unmarshal(saved, &renewed); err != nil {
		t.Fatal(err)
	}
	var files kimiNativeFiles
	if err := json.Unmarshal(renewed.Credential.Payload, &files); err != nil {
		t.Fatal(err)
	}
	var renewedToken kimiToken
	if err := json.Unmarshal([]byte(files.Credentials), &renewedToken); err != nil {
		t.Fatal(err)
	}
	if renewedToken.AccessToken != "rotated-access" || renewedToken.RefreshToken != "rotated-refresh" {
		t.Fatalf("saved credential = %+v", renewedToken)
	}
}

// A Pi account renews its OpenAI grant through the shared refresh, so an expired
// access token must not mark the account as signed out.
func TestExpiredPiAccessTokenIsNotASignOut(t *testing.T) {
	type piEntry struct {
		Type      string `json:"type"`
		Access    string `json:"access"`
		Refresh   string `json:"refresh"`
		Expires   int64  `json:"expires"`
		AccountID string `json:"accountId"`
	}
	entry, err := json.Marshal(map[string]piEntry{"openai-codex": {
		Type: "oauth", Access: "access-token", Refresh: "refresh-token", Expires: time.Now().Add(-time.Hour).UnixMilli(), AccountID: "workspace",
	}})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]json.RawMessage{"auth.json": entry})
	if err != nil {
		t.Fatal(err)
	}
	account := &Account{Harness: "pi"}
	credential := native.Captured{Identity: "pi:openai-codex", Credential: store.Credential{Schema: "pi/native-files@1", Payload: payload}}
	setCredentialExpiry(account, credential)
	if account.NeedsLogin || account.Status == "needs_auth" {
		t.Fatalf("an expired Pi access token asked for a new sign-in: %+v", account)
	}
	if account.ExpiresAt == nil {
		t.Fatal("the access-token expiry was not shown")
	}
}
