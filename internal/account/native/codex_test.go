package native

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func codexTestJWT(v any) string {
	raw, _ := json.Marshal(v)
	return "header." + base64.RawURLEncoding.EncodeToString(raw) + ".signature"
}
func codexTestAuth(user, workspace, refresh string, expiry time.Time) []byte {
	raw, _ := json.Marshal(map[string]any{"auth_mode": "chatgpt", "OPENAI_API_KEY": nil, "tokens": map[string]any{"id_token": codexTestJWT(map[string]any{"https://api.openai.com/auth": map[string]string{"chatgpt_user_id": user, "chatgpt_account_id": workspace}}), "access_token": codexTestJWT(map[string]int64{"exp": expiry.Unix()}), "refresh_token": refresh, "account_id": workspace}, "last_refresh": "2026-01-01T00:00:00Z", "future_field": map[string]bool{"preserve": true}})
	return raw
}
func codexTestManager(t *testing.T) (*codexManager, string) {
	t.Helper()
	home := t.TempDir()
	root := filepath.Join(home, ".codex")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	return &codexManager{host: Host{Home: home, Env: func(string) string { return "" }, Executable: "/fake/codex"}}, root
}
func codexTestWrite(t *testing.T, path string, raw []byte) {
	t.Helper()
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestCodexCaptureAndActivation(t *testing.T) {
	m, root := codexTestManager(t)
	path := filepath.Join(root, "auth.json")
	outgoing := codexTestAuth("person-a", "workspace-a", "fixture-old", time.Now().Add(time.Hour))
	codexTestWrite(t, path, outgoing)
	before, err := m.Capture(context.Background(), "")
	if err != nil || len(before) != 1 || before[0].Identity != "openai:person-a:workspace-a" {
		t.Fatal("capture identity", err)
	}
	next, err := codexParse(codexTestAuth("person-b", "workspace-b", "fixture-new", time.Now().Add(time.Hour)))
	if err != nil {
		t.Fatal(err)
	}
	previous, err := m.Activate(context.Background(), "", next.Credential)
	if err != nil || len(previous) != 1 || previous[0].Identity != before[0].Identity {
		t.Fatal("previous capture", err)
	}
	after, err := m.Capture(context.Background(), "")
	if err != nil || after[0].Identity != next.Identity {
		t.Fatal("target capture", err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("permissions", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(raw), "future_field") {
		t.Fatal("lost native field", err)
	}
	// Capture-back returns the current native rotation, not the earlier snapshot.
	rotated := codexTestAuth("person-b", "workspace-b", "fixture-rotated", time.Now().Add(time.Hour))
	codexTestWrite(t, path, rotated)
	previous, err = m.Activate(context.Background(), "", before[0].Credential)
	if err != nil || len(previous) != 1 {
		t.Fatal(err)
	}
	captured, _, err := codexUnpack(previous[0].Credential)
	if err != nil || string(captured) != string(rotated) {
		t.Fatal("rotation lost", err)
	}
}
func TestCodexRefusesUnsupportedState(t *testing.T) {
	for _, mode := range []string{"keyring", "auto", "ephemeral"} {
		t.Run(mode, func(t *testing.T) {
			m, root := codexTestManager(t)
			codexTestWrite(t, filepath.Join(root, "config.toml"), []byte("cli_auth_credentials_store = \""+mode+"\"\n"))
			_, err := m.Capture(context.Background(), "")
			if !errors.Is(err, ErrUnsupported) {
				t.Fatal(err)
			}
		})
	}
	m, root := codexTestManager(t)
	_, err := m.Capture(context.Background(), "")
	if !errors.Is(err, ErrNotSignedIn) {
		t.Fatal(err)
	}
	raw := codexTestAuth("person", "workspace", "fixture", time.Now().Add(time.Hour))
	codexTestWrite(t, filepath.Join(root, "auth.json"), raw)
	m.host.Env = func(key string) string {
		if key == "CODEX_API_KEY" {
			return "fixture-override"
		}
		return ""
	}
	_, err = m.Capture(context.Background(), "")
	if !errors.Is(err, ErrUnsupported) {
		t.Fatal(err)
	}
	m.host.Env = func(string) string { return "" }
	if err = os.Remove(filepath.Join(root, "auth.json")); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "target")
	codexTestWrite(t, target, raw)
	if err = os.Symlink(target, filepath.Join(root, "auth.json")); err != nil {
		t.Fatal(err)
	}
	_, err = m.Capture(context.Background(), "")
	if err == nil {
		t.Fatal("accepted symlink")
	}
}
func TestCodexIdentityValidation(t *testing.T) {
	for _, user := range []string{"", "email:as-id", "bad\nidentity"} {
		_, err := codexParse(codexTestAuth(user, "workspace", "fixture", time.Now()))
		if err == nil {
			t.Fatal("accepted invalid identity")
		}
	}
	raw := codexTestAuth("person", "workspace", "fixture", time.Now())
	var doc map[string]any
	_ = json.Unmarshal(raw, &doc)
	doc["auth_mode"] = "apiKey"
	raw, _ = json.Marshal(doc)
	_, err := codexParse(raw)
	if !errors.Is(err, ErrUnsupported) {
		t.Fatal(err)
	}
}
func TestCodexLockRespectsContext(t *testing.T) {
	m, root := codexTestManager(t)
	c, err := codexParse(codexTestAuth("person", "workspace", "fixture", time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(root, ".dyna-auth.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	_, err = m.Activate(ctx, "", c.Credential)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}
func TestCodexPreviousSurvivesWriteFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("directory permissions are not enforced for root")
	}
	m, root := codexTestManager(t)
	c, err := codexParse(codexTestAuth("person", "workspace", "fixture", time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	raw, _, _ := codexUnpack(c.Credential)
	codexTestWrite(t, filepath.Join(root, "auth.json"), raw)
	codexTestWrite(t, filepath.Join(root, ".dyna-auth.lock"), nil)
	if err = os.Chmod(root, 0500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(root, 0700)
	previous, err := m.Activate(context.Background(), "", c.Credential)
	if err == nil || len(previous) != 1 || previous[0].Identity != c.Identity {
		t.Fatal("lost previous on failed write", err)
	}
}
func TestCodexRefreshLifecycle(t *testing.T) {
	m, _ := codexTestManager(t)
	var calls atomic.Int32
	fresh, _ := codexParse(codexTestAuth("person", "workspace", "fixture-old", time.Now().Add(time.Hour)))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body map[string]string
		if r.Method != "POST" || r.Header.Get("Content-Type") != "application/json" || json.NewDecoder(r.Body).Decode(&body) != nil || body["refresh_token"] != "fixture-old" || body["client_id"] != "app_EMoamEEZ73f0CkXaXp7hrann" {
			t.Error("unexpected refresh request")
			w.WriteHeader(400)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": codexTestJWT(map[string]int64{"exp": time.Now().Add(time.Hour).Unix()}), "refresh_token": "fixture-rotated"})
	}))
	defer server.Close()
	m.client = server.Client()
	m.tokenURL = server.URL
	// Persisted records have zero ExpiresAt. Read the expiry from their payload.
	fresh.ExpiresAt = time.Time{}
	unchanged, err := m.Refresh(context.Background(), fresh)
	if err != nil || !reflect.DeepEqual(unchanged, fresh) || calls.Load() != 0 {
		t.Fatal("gratuitous refresh", err)
	}
	expired, _ := codexParse(codexTestAuth("person", "workspace", "fixture-old", time.Now().Add(-time.Hour)))
	expired.ExpiresAt = time.Time{}
	renewed, err := m.Refresh(context.Background(), expired)
	if err != nil || renewed.Identity != expired.Identity || calls.Load() != 1 {
		t.Fatal("refresh failed", err)
	}
	raw, _, _ := codexUnpack(renewed.Credential)
	if !strings.Contains(string(raw), "fixture-rotated") || !strings.Contains(string(raw), "future_field") {
		t.Fatal("rotation or fields lost")
	}
}
func TestCodexRefreshErrorsStayRedacted(t *testing.T) {
	for _, status := range []int{400, 401, 403, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			m, _ := codexTestManager(t)
			c, _ := codexParse(codexTestAuth("person", "workspace", "fixture", time.Now().Add(-time.Hour)))
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"error":"invalid_grant","secret":"never-show-this"}`))
			}))
			defer server.Close()
			m.client = server.Client()
			m.tokenURL = server.URL
			_, err := m.Refresh(context.Background(), c)
			if err == nil || strings.Contains(err.Error(), "never-show-this") {
				t.Fatal("unredacted or missing error")
			}
			if status != 500 && !errors.Is(err, ErrInvalidGrant) {
				t.Fatal(err)
			}
		})
	}
}
func TestCodexLoginHasNoSideEffects(t *testing.T) {
	m, _ := codexTestManager(t)
	root := filepath.Join(t.TempDir(), "not-created")
	argv, env, err := m.LoginCommand(root, "")
	if err != nil || len(argv) != 4 || argv[2] != `cli_auth_credentials_store="file"` || !strings.Contains(strings.Join(env, "\n"), "CODEX_HOME="+root) {
		t.Fatal("isolated login", err)
	}
	if _, err = os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("login command created files")
	}
	argv, env, err = m.LoginCommand("", "")
	if err != nil || !reflect.DeepEqual(argv, []string{"/fake/codex", "login"}) || len(env) != 0 {
		t.Fatal("in-place login", err)
	}
}

func TestCodexRefreshRejectsAccountDriftAndRedirect(t *testing.T) {
	for _, redirect := range []bool{false, true} {
		t.Run(map[bool]string{true: "redirect", false: "identity"}[redirect], func(t *testing.T) {
			m, _ := codexTestManager(t)
			c, _ := codexParse(codexTestAuth("person", "workspace", "fixture", time.Now().Add(-time.Hour)))
			var redirects atomic.Int32
			destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirects.Add(1) }))
			defer destination.Close()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if redirect {
					http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]string{"access_token": codexTestJWT(map[string]int64{"exp": time.Now().Add(time.Hour).Unix()}), "id_token": codexTestJWT(map[string]any{"https://api.openai.com/auth": map[string]string{"chatgpt_user_id": "another-person", "chatgpt_account_id": "workspace"}}), "refresh_token": "fixture-rotated"})
			}))
			defer server.Close()
			m.client = server.Client()
			m.tokenURL = server.URL
			if _, err := m.Refresh(context.Background(), c); err == nil {
				t.Fatal("accepted changed account or redirect")
			}
			if redirects.Load() != 0 {
				t.Fatal("sent grant to redirect destination")
			}
		})
	}
}

func TestCodexAmbiguousStoreSettingFailsClosed(t *testing.T) {
	for _, config := range []string{
		`"cli_auth_credentials_\u0073tore" = "keyring"`,
		"[profiles.other]\ncli_auth_credentials_store = \"keyring\"\n",
		"cli_auth_credentials_store = \"file\"\ncli_auth_credentials_store = \"keyring\"\n",
	} {
		m, root := codexTestManager(t)
		codexTestWrite(t, filepath.Join(root, "config.toml"), []byte(config))
		_, err := m.Capture(context.Background(), "")
		if !errors.Is(err, ErrUnsupported) {
			t.Fatal("accepted ambiguous auth store", err)
		}
	}
}

func TestCodexBadRequestOnlyInvalidatesExplicitRefreshGrant(t *testing.T) {
	cases := []struct {
		name, body string
		invalid    bool
	}{
		{"invalid grant", `{"error":"invalid_grant"}`, true},
		{"expired refresh", `{"error":"invalid_request","error_description":"The refresh token has expired."}`, true},
		{"revoked refresh", `{"error":"invalid_request","error_description":"The refresh_token was revoked."}`, true},
		{"nested revoked refresh", `{"error":{"code":"invalid_request","message":"The refresh token was revoked."}}`, true},
		{"unrelated request", `{"error":"invalid_request","error_description":"Missing client parameter. never-show-this"}`, false},
		{"other expired token", `{"error":"invalid_request","error_description":"Access token expired."}`, false},
		{"service error", `{"error":"service_error","error_description":"never-show-this"}`, false},
		{"malformed error", `never-show-this`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := codexTestManager(t)
			c, _ := codexParse(codexTestAuth("person", "workspace", "fixture", time.Now().Add(-time.Hour)))
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			m.client = server.Client()
			m.tokenURL = server.URL
			_, err := m.Refresh(context.Background(), c)
			if err == nil || errors.Is(err, ErrInvalidGrant) != tc.invalid {
				t.Fatalf("wrong grant classification: %v", err)
			}
			if strings.Contains(err.Error(), "never-show-this") {
				t.Fatal("raw provider error escaped")
			}
		})
	}
}
