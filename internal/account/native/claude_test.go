package native

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"dyna-agent/internal/account/store"
)

const claudeTestAccountA = "11111111-1111-4111-8111-111111111111"
const claudeTestAccountB = "22222222-2222-4222-8222-222222222222"

func claudeTestManager(t *testing.T, env map[string]string) *claudeManager {
	t.Helper()
	h := Host{Home: t.TempDir(), Executable: "/test/claude", Env: func(k string) string { return env[k] }}
	manager, ok := For("claude-code", h)
	if !ok {
		t.Fatal("Claude adapter is not registered")
	}
	m := manager.(*claudeManager)
	m.now = func() time.Time { return time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC) }
	return m
}
func claudeTestWrite(t *testing.T, path string, v any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal("Cannot create fixture directory")
	}
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal("Cannot encode fixture")
	}
	if err = os.WriteFile(path, b, 0600); err != nil {
		t.Fatal("Cannot write fixture")
	}
}
func claudeTestPayload(t *testing.T, m *claudeManager, id, grant string, remaining time.Duration) store.Credential {
	t.Helper()
	tok, _ := json.Marshal(map[string]any{"accessToken": "fixture-access-" + grant, "refreshToken": "fixture-refresh-" + grant, "expiresAt": m.now().Add(remaining).UnixMilli(), "refreshTokenExpiresAt": m.now().Add(24 * time.Hour).UnixMilli(), "scopes": []string{"user:inference", "user:profile"}, "subscriptionType": "max", "rateLimitTier": "fixture-tier", "futureField": "keep"})
	account, _ := json.Marshal(map[string]any{"accountUuid": id, "emailAddress": "private-fixture@example.invalid", "organizationUuid": "fixture-org", "displayName": "Private fixture", "futureAccountField": true})
	b, _ := json.Marshal(claudePayload{Credentials: map[string]json.RawMessage{"claudeAiOauth": tok}, OAuthAccount: account})
	return store.Credential{Schema: claudeSchema, Payload: b}
}
func claudeTestInstall(t *testing.T, m *claudeManager, root string, c store.Credential) claudePaths {
	t.Helper()
	p, err := m.paths(root)
	if err != nil {
		t.Fatal("Cannot resolve fixture paths")
	}
	payload, _, _, _, err := claudeDecode(c)
	if err != nil {
		t.Fatal("Invalid fixture credential")
	}
	claudeTestWrite(t, p.credentials, map[string]any{"claudeAiOauth": payload.Credentials["claudeAiOauth"], "mcpOAuth": map[string]any{"fixture": "preserve"}})
	claudeTestWrite(t, p.config, map[string]any{"oauthAccount": payload.OAuthAccount, "projects": map[string]any{"fixture": true}, "theme": "preserve"})
	return p
}
func claudeTestCapture(t *testing.T, m *claudeManager, root string) Captured {
	t.Helper()
	got, err := m.Capture(context.Background(), root)
	if err != nil || len(got) != 1 {
		t.Fatal("Cannot capture fixture account")
	}
	return got[0]
}
func TestClaudeCapturePathsAndNoMutation(t *testing.T) {
	for _, mode := range []string{"default", "isolated", "config-override", "secret-override", "legacy"} {
		t.Run(mode, func(t *testing.T) {
			env := map[string]string{}
			m := claudeTestManager(t, env)
			root := ""
			switch mode {
			case "isolated":
				root = filepath.Join(m.host.HomeDir(), "isolated")
				env["CLAUDE_CODE_OAUTH_TOKEN"] = "fixture-shadow"
			case "config-override":
				env["CLAUDE_CONFIG_DIR"] = filepath.Join(m.host.HomeDir(), "config")
			case "secret-override":
				env["CLAUDE_SECURESTORAGE_CONFIG_DIR"] = filepath.Join(m.host.HomeDir(), "secrets")
			case "legacy":
				claudeTestWrite(t, filepath.Join(m.host.HomeDir(), ".claude", ".config.json"), map[string]any{"theme": "fixture"})
			}
			c := claudeTestPayload(t, m, claudeTestAccountA, "a", time.Hour)
			p := claudeTestInstall(t, m, root, c)
			before, _ := os.Stat(p.credentials)
			beforeConfig, _ := os.ReadFile(p.config)
			got := claudeTestCapture(t, m, root)
			if got.Identity != "anthropic:"+claudeTestAccountA || got.ExpiresAt.IsZero() {
				t.Fatal("Capture did not bind identity and expiry")
			}
			after, _ := os.Stat(p.credentials)
			afterConfig, _ := os.ReadFile(p.config)
			if !before.ModTime().Equal(after.ModTime()) || string(beforeConfig) != string(afterConfig) {
				t.Fatal("Capture changed native files")
			}
			if mode == "legacy" && p.config != filepath.Join(m.host.HomeDir(), ".claude.json") {
				t.Fatal("Current metadata path was not selected")
			}
			if mode == "config-override" && p.config != filepath.Join(env["CLAUDE_CONFIG_DIR"], ".claude.json") {
				t.Fatal("Custom metadata path was not selected")
			}
		})
	}
}
func TestClaudeCaptureRejectsUnknownAndConflictingAuth(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		m := claudeTestManager(t, nil)
		_, err := m.Capture(context.Background(), "")
		if !errors.Is(err, ErrNotSignedIn) {
			t.Fatal("Missing account should require sign-in")
		}
	})
	for _, key := range []string{"CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN_FILE_DESCRIPTOR", "ANTHROPIC_PROFILE", "CLAUDE_CODE_USE_BEDROCK"} {
		t.Run(key, func(t *testing.T) {
			value := "fixture"
			if strings.Contains(key, "DESCRIPTOR") {
				value = "0"
			}
			m := claudeTestManager(t, map[string]string{key: value})
			_, err := m.Capture(context.Background(), "")
			if !errors.Is(err, ErrUnsupported) {
				t.Fatal("Injected auth was not rejected")
			}
		})
	}
	for _, mode := range []string{"missing-identity", "gateway", "api-key", "helper", "console-profile", "symlink", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			m := claudeTestManager(t, nil)
			p := claudeTestInstall(t, m, "", claudeTestPayload(t, m, claudeTestAccountA, "a", time.Hour))
			want := ErrUnsupported
			switch mode {
			case "missing-identity":
				claudeTestWrite(t, p.config, map[string]any{"theme": "fixture"})
				want = ErrNoIdentity
			case "gateway":
				claudeTestWrite(t, p.credentials, map[string]any{"enterpriseGateway": map[string]any{"jwt": "fixture"}})
			case "api-key":
				claudeTestWrite(t, p.config, map[string]any{"primaryApiKey": "fixture"})
			case "helper":
				claudeTestWrite(t, filepath.Join(p.dir, "settings.json"), map[string]any{"apiKeyHelper": "fixture-helper"})
			case "console-profile":
				claudeTestWrite(t, filepath.Join(m.host.HomeDir(), ".config", "anthropic", "configs", "default.json"), map[string]any{"authentication": map[string]any{"type": "user_oauth"}})
			case "symlink":
				os.Remove(p.credentials)
				if os.Symlink(p.config, p.credentials) != nil {
					t.Fatal("Cannot create fixture symlink")
				}
				want = nil
			case "malformed":
				if os.WriteFile(p.credentials, []byte("fixture-not-json"), 0600) != nil {
					t.Fatal("Cannot write malformed fixture")
				}
				want = ErrSchema
			}
			_, err := m.Capture(context.Background(), "")
			if err == nil || want != nil && !errors.Is(err, want) {
				t.Fatal("Conflicting or malformed native state was accepted")
			}
		})
	}
}
func TestClaudeActivateCapturesPreviousAndPreservesOtherSettings(t *testing.T) {
	m := claudeTestManager(t, nil)
	old := claudeTestPayload(t, m, claudeTestAccountA, "newest-outgoing", time.Hour)
	p := claudeTestInstall(t, m, "", old)
	target := claudeTestPayload(t, m, claudeTestAccountB, "b", 2*time.Hour)
	previous, err := m.Activate(context.Background(), "", target)
	if err != nil || len(previous) != 1 {
		t.Fatal("Activation did not return outgoing account")
	}
	_, tok, _, _, _ := claudeDecode(previous[0].Credential)
	if tok.RefreshToken != "fixture-refresh-newest-outgoing" {
		t.Fatal("Activation lost the latest outgoing grant")
	}
	got := claudeTestCapture(t, m, "")
	if got.Identity != "anthropic:"+claudeTestAccountB {
		t.Fatal("Target account was not installed")
	}
	creds, _ := claudeReadObject(p.credentials)
	config, _ := claudeReadObject(p.config)
	if len(creds["mcpOAuth"]) == 0 || len(config["projects"]) == 0 || string(config["theme"]) != `"preserve"` {
		t.Fatal("Activation removed unrelated state")
	}
	for _, path := range []string{p.credentials, p.config} {
		st, e := os.Stat(path)
		if e != nil || st.Mode().Perm() != 0600 {
			t.Fatal("Native auth mode is not private")
		}
	}
	for _, path := range []string{filepath.Join(p.secretDir, ".oauth_refresh.lock"), p.secretDir + ".lock", p.config + ".lock", filepath.Join(p.secretDir, ".storage-write.lock")} {
		if _, e := os.Lstat(path); !os.IsNotExist(e) {
			t.Fatal("Activation left a native lock")
		}
	}
}
func TestClaudeActivateCapturesRotationAfterWaitingForNativeLock(t *testing.T) {
	m := claudeTestManager(t, nil)
	p := claudeTestInstall(t, m, "", claudeTestPayload(t, m, claudeTestAccountA, "old", time.Hour))
	lock := filepath.Join(p.secretDir, ".oauth_refresh.lock")
	if os.Mkdir(lock, 0700) != nil {
		t.Fatal("Cannot hold native lock")
	}
	target := claudeTestPayload(t, m, claudeTestAccountB, "b", time.Hour)
	type result struct {
		previous []Captured
		err      error
	}
	done := make(chan result, 1)
	go func() { previous, err := m.Activate(context.Background(), "", target); done <- result{previous, err} }()
	// The fixture changes while the native refresh lock excludes activation.
	claudeTestInstall(t, m, "", claudeTestPayload(t, m, claudeTestAccountA, "rotated", time.Hour))
	if os.Remove(lock) != nil {
		t.Fatal("Cannot release native lock")
	}
	select {
	case out := <-done:
		if out.err != nil || len(out.previous) != 1 {
			t.Fatal("Activation after rotation failed")
		}
		_, tok, _, _, _ := claudeDecode(out.previous[0].Credential)
		if tok.RefreshToken != "fixture-refresh-rotated" {
			t.Fatal("Outgoing rotation was lost")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Activation did not finish after lock release")
	}
}
func TestClaudeActivateRespectsAllNativeLocks(t *testing.T) {
	for _, which := range []int{0, 1, 2, 3} {
		t.Run(string(rune('a'+which)), func(t *testing.T) {
			m := claudeTestManager(t, nil)
			p := claudeTestInstall(t, m, "", claudeTestPayload(t, m, claudeTestAccountA, "a", time.Hour))
			paths := []string{filepath.Join(p.secretDir, ".oauth_refresh.lock"), p.secretDir + ".lock", p.config + ".lock", filepath.Join(p.secretDir, ".storage-write.lock")}
			if os.Mkdir(paths[which], 0700) != nil {
				t.Fatal("Cannot hold fixture lock")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
			defer cancel()
			_, err := m.Activate(ctx, "", claudeTestPayload(t, m, claudeTestAccountB, "b", time.Hour))
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal("Activation ignored native lock")
			}
			if _, err = os.Stat(paths[which]); err != nil {
				t.Fatal("Activation removed another writer's lock")
			}
			if claudeTestCapture(t, m, "").Identity != "anthropic:"+claudeTestAccountA {
				t.Fatal("Blocked activation changed native account")
			}
		})
	}
}
func TestClaudeActivateReturnsPreviousAndRestoresOnWriteFailure(t *testing.T) {
	m := claudeTestManager(t, nil)
	p := claudeTestInstall(t, m, "", claudeTestPayload(t, m, claudeTestAccountA, "a", time.Hour))
	failed := false
	m.write = func(path string, obj map[string]json.RawMessage) error {
		if path == p.config && !failed {
			failed = true
			return errors.New("Fixture write failure.")
		}
		return claudeAtomicJSON(path, obj)
	}
	previous, err := m.Activate(context.Background(), "", claudeTestPayload(t, m, claudeTestAccountB, "b", time.Hour))
	if !errors.Is(err, ErrPartialActivation) || len(previous) != 1 || previous[0].Identity != "anthropic:"+claudeTestAccountA {
		t.Fatal("Failed activation did not return outgoing snapshot")
	}
	if claudeTestCapture(t, m, "").Identity != "anthropic:"+claudeTestAccountA {
		t.Fatal("Failed activation did not restore outgoing pair")
	}
}
func TestClaudeActivateRefusesToOverwriteUnboundAccount(t *testing.T) {
	m := claudeTestManager(t, nil)
	p := claudeTestInstall(t, m, "", claudeTestPayload(t, m, claudeTestAccountA, "a", time.Hour))
	claudeTestWrite(t, p.config, map[string]any{"theme": "fixture"})
	before, _ := os.ReadFile(p.credentials)
	_, err := m.Activate(context.Background(), "", claudeTestPayload(t, m, claudeTestAccountB, "b", time.Hour))
	if !errors.Is(err, ErrNoIdentity) {
		t.Fatal("Unbound outgoing account was overwritten")
	}
	after, _ := os.ReadFile(p.credentials)
	if string(before) != string(after) {
		t.Fatal("Refused activation changed credentials")
	}
}
func TestClaudeLockDoesNotRemoveReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fixture.lock")
	lock, err := claudeLock(context.Background(), path)
	if err != nil {
		t.Fatal("Cannot acquire fixture lock")
	}
	if os.Rename(path, path+".old") != nil || os.Mkdir(path, 0700) != nil {
		t.Fatal("Cannot replace fixture lock")
	}
	if lock.check() == nil {
		t.Fatal("Lock compromise was not detected")
	}
	lock.close()
	if _, err = os.Stat(path); err != nil {
		t.Fatal("Release removed a replacement lock")
	}
}
func TestClaudeRefreshUsesPayloadExpiryAndAvoidsActiveGrant(t *testing.T) {
	m := claudeTestManager(t, nil)
	fresh := claudeTestPayload(t, m, claudeTestAccountA, "fresh", time.Hour)
	_, _, id, _, _ := claudeDecode(fresh)
	c := Captured{Identity: id, Credential: fresh}
	got, err := m.Refresh(context.Background(), c)
	if err != nil || !reflect.DeepEqual(got, c) {
		t.Fatal("Fresh persisted payload was changed or refreshed")
	}
	expired := claudeTestPayload(t, m, claudeTestAccountA, "expired", -time.Minute)
	claudeTestInstall(t, m, "", expired)
	c.Credential = expired
	_, err = m.Refresh(context.Background(), c)
	if !errors.Is(err, ErrUnsupported) {
		t.Fatal("Active native grant was refreshed from a saved copy")
	}
}
func TestClaudeRefreshRotationAndScopePreservation(t *testing.T) {
	for _, returnScope := range []bool{false, true} {
		t.Run(map[bool]string{false: "retained-scope", true: "returned-scope"}[returnScope], func(t *testing.T) {
			m := claudeTestManager(t, nil)
			c := Captured{Identity: "anthropic:" + claudeTestAccountA, Credential: claudeTestPayload(t, m, claudeTestAccountA, "inactive", -time.Minute)}
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				var body map[string]any
				if r.Method != http.MethodPost || r.Header.Get("User-Agent") != claudeUserAgent || r.Header.Get("Content-Type") != "application/json" || json.NewDecoder(r.Body).Decode(&body) != nil || body["client_id"] != claudeClientID || body["grant_type"] != "refresh_token" || body["refresh_token"] != "fixture-refresh-inactive" {
					t.Error("Refresh request does not match native OAuth contract")
				}
				out := map[string]any{"access_token": "fixture-access-new", "refresh_token": "fixture-refresh-new", "expires_in": 3600, "refresh_token_expires_in": 86400}
				if returnScope {
					out["scope"] = "user:inference user:profile user:file_upload"
				}
				_ = json.NewEncoder(w).Encode(out)
			}))
			defer server.Close()
			m.client = server.Client()
			m.tokenURL = server.URL
			got, err := m.Refresh(context.Background(), c)
			if err != nil {
				t.Fatal("Inactive refresh failed")
			}
			p, tok, _, _, err := claudeDecode(got.Credential)
			if err != nil || tok.AccessToken != "fixture-access-new" || tok.RefreshToken != "fixture-refresh-new" || !got.ExpiresAt.Equal(m.now().Add(time.Hour)) || calls.Load() != 1 {
				t.Fatal("Refreshed grant was not retained as one generation")
			}
			var fields map[string]json.RawMessage
			_ = json.Unmarshal(p.Credentials["claudeAiOauth"], &fields)
			if string(fields["futureField"]) != `"keep"` || string(fields["subscriptionType"]) != `"max"` {
				t.Fatal("Refresh dropped native metadata")
			}
			wantScopes := 2
			if returnScope {
				wantScopes = 3
			}
			if len(tok.Scopes) != wantScopes {
				t.Fatal("Refresh lost scope information")
			}
			_, err = m.Refresh(context.Background(), got)
			if err != nil || calls.Load() != 1 {
				t.Fatal("Valid rotated grant was refreshed again")
			}
		})
	}
}
func TestClaudeRefreshErrorsDoNotExposeNativeOutput(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		body    string
		invalid bool
	}{
		{"invalid-grant", 400, `{"error":"invalid_grant","error_description":"fixture-private-do-not-log"}`, true},
		{"rate-limit", 429, `{"error":"fixture-private-do-not-log"}`, false},
		{"malformed", 200, `fixture-private-do-not-log`, false},
		{"redirect", 302, `{"error":"fixture-private-do-not-log"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := claudeTestManager(t, nil)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			m.client = server.Client()
			m.tokenURL = server.URL
			c := Captured{Identity: "anthropic:" + claudeTestAccountA, Credential: claudeTestPayload(t, m, claudeTestAccountA, "inactive", -time.Minute)}
			_, err := m.Refresh(context.Background(), c)
			if err == nil || strings.Contains(err.Error(), "fixture-private") || tc.invalid && !errors.Is(err, ErrInvalidGrant) {
				t.Fatal("Refresh error exposed output or lost invalid-grant status")
			}
		})
	}
}
func TestClaudeLoginCommandHasNoSideEffects(t *testing.T) {
	m := claudeTestManager(t, nil)
	root := filepath.Join(m.host.HomeDir(), "isolated-not-created")
	argv, env, err := m.LoginCommand(root, "")
	if err != nil || !reflect.DeepEqual(argv, []string{"/test/claude", "auth", "login", "--claudeai"}) {
		t.Fatal("Unexpected isolated login command")
	}
	for _, want := range []string{"CLAUDE_CONFIG_DIR=" + root, "CLAUDE_SECURESTORAGE_CONFIG_DIR=" + root, "ANTHROPIC_CONFIG_DIR=" + filepath.Join(root, "anthropic"), "CLAUDE_CODE_OAUTH_TOKEN=", "ANTHROPIC_API_KEY="} {
		found := false
		for _, v := range env {
			if v == want {
				found = true
			}
		}
		if !found {
			t.Fatal("Isolated login did not set safe environment overrides")
		}
	}
	if _, err = os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("Login command construction touched the filesystem")
	}
	_, env, err = m.LoginCommand("", "")
	if err != nil {
		t.Fatal("In-place native login is unavailable")
	}
	for _, v := range env {
		if strings.HasPrefix(v, "CLAUDE_CONFIG_DIR=") {
			t.Fatal("In-place login relocated the default global config")
		}
	}
	if _, _, err = m.LoginCommand("relative-root", ""); !errors.Is(err, ErrUnsupported) {
		t.Fatal("Relative isolated root was accepted")
	}
}

func TestImportLegacyClaudeReadsOnlyBoundHelperSnapshots(t *testing.T) {
	for _, custom := range []bool{false, true} {
		t.Run(map[bool]string{false: "default-state", true: "custom-state"}[custom], func(t *testing.T) {
			env := map[string]string{}
			m := claudeTestManager(t, env)
			state := filepath.Join(m.host.HomeDir(), ".local", "state")
			if custom {
				state = filepath.Join(m.host.HomeDir(), "custom-state")
				env["XDG_STATE_HOME"] = state
			}
			root := filepath.Join(state, "claude-accounts")
			good := claudeTestPayload(t, m, claudeTestAccountA, "legacy", -time.Hour)
			p, _, _, _, _ := claudeDecode(good)
			dir := filepath.Join(root, claudeTestAccountA)
			claudeTestWrite(t, filepath.Join(dir, ".credentials.json"), p.Credentials)
			var account map[string]json.RawMessage
			_ = json.Unmarshal(p.OAuthAccount, &account)
			claudeTestWrite(t, filepath.Join(dir, "oauthAccount.json"), account)
			claudeTestWrite(t, filepath.Join(dir, "meta.json"), map[string]any{"uuid": claudeTestAccountA, "label": "Private legacy label"})
			// A mismatched directory, missing metadata block, malformed credential,
			// and symlink must not become activatable saved accounts.
			wrong := filepath.Join(root, claudeTestAccountB)
			claudeTestWrite(t, filepath.Join(wrong, ".credentials.json"), p.Credentials)
			claudeTestWrite(t, filepath.Join(wrong, "oauthAccount.json"), account)
			missing := filepath.Join(root, "33333333-3333-4333-8333-333333333333")
			claudeTestWrite(t, filepath.Join(missing, ".credentials.json"), p.Credentials)
			malformed := filepath.Join(root, "44444444-4444-4444-8444-444444444444")
			claudeTestWrite(t, filepath.Join(malformed, ".credentials.json"), map[string]any{"claudeAiOauth": "invalid"})
			claudeTestWrite(t, filepath.Join(malformed, "oauthAccount.json"), account)
			if os.Symlink(dir, filepath.Join(root, "55555555-5555-4555-8555-555555555555")) != nil {
				t.Fatal("Cannot create helper symlink fixture")
			}
			credPath := filepath.Join(dir, ".credentials.json")
			before, _ := os.ReadFile(credPath)
			st, _ := os.Stat(credPath)
			got, err := ImportLegacyClaude(context.Background(), m.host)
			if err != nil || len(got) != 1 || got[0].Identity != "anthropic:"+claudeTestAccountA || got[0].Label != "Private legacy label" {
				t.Fatal("Legacy import did not skip unbound entries")
			}
			_, tokens, _, _, err := claudeDecode(got[0].Credential)
			if err != nil || tokens.RefreshToken != "fixture-refresh-legacy" || got[0].ExpiresAt.After(m.now()) {
				t.Fatal("Legacy import changed or refreshed the saved grant")
			}
			after, _ := os.ReadFile(credPath)
			afterStat, _ := os.Stat(credPath)
			if string(before) != string(after) || !st.ModTime().Equal(afterStat.ModTime()) {
				t.Fatal("Legacy import changed the helper store")
			}
			if _, err = os.Stat(filepath.Join(m.host.HomeDir(), ".claude")); !os.IsNotExist(err) {
				t.Fatal("Legacy import touched native state")
			}
		})
	}
}
func TestImportLegacyClaudeMissingAndCancelled(t *testing.T) {
	m := claudeTestManager(t, nil)
	got, err := ImportLegacyClaude(context.Background(), m.host)
	if err != nil || len(got) != 0 {
		t.Fatal("Missing helper store should import no accounts")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = ImportLegacyClaude(ctx, m.host); !errors.Is(err, context.Canceled) {
		t.Fatal("Legacy import ignored cancellation")
	}
}

func TestClaudeCapsRejectsConflictingModes(t *testing.T) {
	m := claudeTestManager(t, nil)
	caps := m.Caps()
	if !caps.Activate || !caps.IsolatedLogin || !caps.Refresh || !strings.Contains(caps.RunningEffect, "Running sessions may use the new account") {
		t.Fatal("Subscription capabilities do not explain the running effect")
	}
	p := claudeTestInstall(t, m, "", claudeTestPayload(t, m, claudeTestAccountA, "a", time.Hour))
	claudeTestWrite(t, p.credentials, map[string]any{"enterpriseGateway": map[string]any{"jwt": "fixture"}})
	caps = m.Caps()
	if caps.Activate || caps.Refresh || !caps.IsolatedLogin {
		t.Fatal("Gateway mode was advertised as subscription activation")
	}
}

func TestClaudeActivateFailureBeforeWriteIsNotPartial(t *testing.T) {
	m := claudeTestManager(t, nil)
	claudeTestInstall(t, m, "", claudeTestPayload(t, m, claudeTestAccountA, "a", time.Hour))
	m.write = func(string, map[string]json.RawMessage) error {
		return errors.New("Fixture write refused before publication.")
	}
	previous, err := m.Activate(context.Background(), "", claudeTestPayload(t, m, claudeTestAccountB, "b", time.Hour))
	if err == nil || errors.Is(err, ErrPartialActivation) || len(previous) != 1 {
		t.Fatal("A rejected first write was classified as partial activation")
	}
	if claudeTestCapture(t, m, "").Identity != "anthropic:"+claudeTestAccountA {
		t.Fatal("Rejected first write changed native auth")
	}
}
func TestClaudeActivateFirstWritePostRenameFailureIsPartial(t *testing.T) {
	m := claudeTestManager(t, nil)
	claudeTestInstall(t, m, "", claudeTestPayload(t, m, claudeTestAccountA, "a", time.Hour))
	m.write = func(path string, object map[string]json.RawMessage) error {
		if err := claudeAtomicJSON(path, object); err != nil {
			return err
		}
		return ErrPartialActivation
	}
	previous, err := m.Activate(context.Background(), "", claudeTestPayload(t, m, claudeTestAccountB, "b", time.Hour))
	if !errors.Is(err, ErrPartialActivation) || len(previous) != 1 {
		t.Fatal("Post-rename error lost partial activation or outgoing grant")
	}
}
