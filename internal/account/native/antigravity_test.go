package native

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func agyTestWrapper(t *testing.T, sub string) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"token": map[string]string{"access_token": "fake-access-" + sub, "refresh_token": "fake-refresh-" + sub, "expiry": "2100-01-01T00:00:00Z"}, "id_token": testJWT(sub, 4102444800), "auth_method": "consumer", "project_id": "fake-project", "region": "fake-region", "user_tier": "fake-tier", "future_field": map[string]bool{"keep": true}})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func agyTestManager(t *testing.T) *antigravityManager {
	t.Helper()
	return &antigravityManager{host: testHost(t, nil)}
}

func TestAntigravityRefusesIdentitylessOrUnsupportedWrapper(t *testing.T) {
	raw := agyTestWrapper(t, "source")
	var doc map[string]json.RawMessage
	_ = json.Unmarshal(raw, &doc)
	delete(doc, "id_token")
	missing, _ := json.Marshal(doc)
	if _, err := agyAccount(missing); !errors.Is(err, ErrNoIdentity) {
		t.Fatal("accepted wrapper without ID token")
	}
	doc["id_token"] = json.RawMessage(`"malformed"`)
	bad, _ := json.Marshal(doc)
	if _, err := agyAccount(bad); !errors.Is(err, ErrNoIdentity) {
		t.Fatal("accepted malformed ID token")
	}
	_ = json.Unmarshal(raw, &doc)
	doc["auth_method"] = json.RawMessage(`"unsupported"`)
	bad, _ = json.Marshal(doc)
	if _, err := agyAccount(bad); !errors.Is(err, ErrUnsupported) {
		t.Fatal("accepted unknown auth method")
	}
	doc["auth_method"] = json.RawMessage(`"gcp"`)
	delete(doc, "project_id")
	bad, _ = json.Marshal(doc)
	if _, err := agyAccount(bad); !errors.Is(err, ErrSchema) {
		t.Fatal("accepted incomplete GCP onboarding")
	}
}
func TestAntigravityLoginAndRefreshCapabilities(t *testing.T) {
	m := &antigravityManager{host: testHost(t, nil)}
	m.host.Executable = "/test/agy"
	argv, env, err := m.LoginCommand("", "")
	if err != nil || len(argv) != 1 || argv[0] != "/test/agy" || len(env) == 0 {
		t.Fatal("in-place command failed")
	}
	root := t.TempDir()
	_, isolated, err := m.LoginCommand(root, "")
	if err != nil || !strings.Contains(strings.Join(isolated, "\n"), "HOME="+root) {
		t.Fatal("isolated login failed", err)
	}
	if _, err := m.Refresh(context.Background(), Captured{}); !errors.Is(err, ErrSchema) {
		t.Fatal("refresh accepted an invalid schema")
	}
	if !m.Caps().IsolatedLogin || !m.Caps().Refresh {
		t.Fatal("incorrect capabilities")
	}
	if _, err := m.Capture(context.Background(), t.TempDir()); !errors.Is(err, ErrNotSignedIn) {
		t.Fatal("isolated capture failed")
	}
}
func TestAntigravityFileRefusesLinkedCredentials(t *testing.T) {
	root := t.TempDir()
	m := &antigravityManager{host: testHost(t, nil)}
	owner := filepath.Join(t.TempDir(), "owner-token")
	testWrite(t, owner, json.RawMessage(agyACPTestToken("owner")))
	path := filepath.Join(root, AntigravityACPTokenFile)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(owner, path); err != nil {
		t.Fatal(err)
	}
	testWrite(t, filepath.Join(root, AntigravityAccountFile), map[string]string{"identity": "google:owner"})
	if _, err := m.Capture(context.Background(), root); err == nil {
		t.Fatal("followed credential symlink")
	}
	if err := (agyFileStore{path: path}).Write(context.Background(), agyACPTestToken("target")); err == nil {
		t.Fatal("overwrote credential symlink")
	}
}

func TestAntigravityRegistration(t *testing.T) {
	m, ok := For("antigravity", testHost(t, nil))
	if !ok || m.Harness() != "antigravity" || !m.Caps().Activate {
		t.Fatal("registration failed")
	}
}

// testJWT builds an unsigned test token with a subject and expiry.
func testJWT(sub string, exp int64) string {
	raw, _ := json.Marshal(map[string]any{"sub": sub, "exp": exp})
	return "e30." + base64.RawURLEncoding.EncodeToString(raw) + ".test-signature"
}

func testHost(t *testing.T, env map[string]string) Host {
	t.Helper()
	return Host{Home: t.TempDir(), Env: func(k string) string { return env[k] }}
}

func testWrite(t *testing.T, path string, value any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
}
