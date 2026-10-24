package native

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestKimiCodeCaptureActivatePreservesManagedLogin(t *testing.T) {
	source := t.TempDir()
	config := []byte(`default_model = "kimi-code/k3"

[providers."managed:kimi-code"]
type = "kimi"
base_url = "https://api.kimi.ai/coding/v1"
api_key = ""

[providers."managed:kimi-code".oauth]
storage = "file"
key = "oauth/kimi-code-env-0123456789abcdef"
oauthHost = "https://auth.kimi.ai"

[models."kimi-code/k3"]
provider = "managed:kimi-code"
model = "k3"
max_context_size = 1048576
support_efforts = ["low", "high", "max"]
default_effort = "max"
`)
	tokenName := "kimi-code-env-0123456789abcdef.json"
	token := []byte(`{"access_token":"fixture-access","refresh_token":"fixture-refresh","expires_at":2000000000,"scope":"coding","token_type":"Bearer","expires_in":3600}`)
	writeKimiFixture := func(root string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(root, "credentials"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "config.toml"), config, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "credentials", tokenName), token, 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeKimiFixture(source)
	manager, ok := For("kimi-code", Host{Home: source, Env: func(key string) string {
		if key == "KIMI_CODE_HOME" {
			return source
		}
		return ""
	}})
	if !ok {
		t.Fatal("Kimi Code has no native account manager")
	}
	captured, err := manager.Capture(context.Background(), "")
	if err != nil || len(captured) != 1 {
		t.Fatalf("Capture = %+v, %v", captured, err)
	}
	login := captured[0]
	if login.Identity != "kimi-code:native" || login.ExpiresAt != time.Unix(2000000000, 0).UTC() {
		t.Fatalf("captured identity or expiry = %+v", login)
	}
	if got := CredentialExpiry(login.Credential); !got.Equal(login.ExpiresAt) {
		t.Fatalf("CredentialExpiry = %v, want %v", got, login.ExpiresAt)
	}

	target := t.TempDir()
	targetManager, ok := For("kimi-code", Host{Home: target, Env: func(key string) string {
		if key == "KIMI_CODE_HOME" {
			return target
		}
		return ""
	}})
	if !ok {
		t.Fatal("Kimi Code target has no native account manager")
	}
	previous, err := targetManager.Activate(context.Background(), "", login.Credential)
	if err != nil || len(previous) != 0 {
		t.Fatalf("Activate previous = %+v, %v", previous, err)
	}
	previous, err = targetManager.Activate(context.Background(), "", login.Credential)
	if err != nil || len(previous) != 1 || !previous[0].ExpiresAt.Equal(login.ExpiresAt) {
		t.Fatalf("second Activate did not preserve the outgoing login: %+v, %v", previous, err)
	}
	for path, want := range map[string][]byte{
		filepath.Join(target, "config.toml"):            config,
		filepath.Join(target, "credentials", tokenName): token,
	} {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != string(want) {
			t.Fatalf("activated %s differs or is unreadable: %v", path, err)
		}
	}
}
