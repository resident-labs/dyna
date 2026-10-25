package runtime

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"dyna-agent/internal/account/store"
)

func TestZZProbe3(t *testing.T) {
	a := testAccounts(t)
	n, err := a.Create("kimi-code", "saved")
	if err != nil {
		t.Fatal(err)
	}
	config := `[providers."managed:kimi-code"]
type = "kimi"
base_url = "https://api.kimi.ai/coding/v1"

[providers."managed:kimi-code".oauth]
storage = "file"
key = "oauth/kimi-code"
oauthHost = "https://auth.kimi.ai"
`
	token := `{"access_token":"a","refresh_token":"r","expires_at":1900003600,"token_type":"Bearer"}`
	shared, _ := a.HarnessHome("kimi-code")
	for _, dir := range []string{shared} {
		os.MkdirAll(filepath.Join(dir, "credentials"), 0700)
		os.WriteFile(filepath.Join(dir, "config.toml"), []byte(config), 0600)
		os.WriteFile(filepath.Join(dir, "credentials", "kimi-code.json"), []byte(token), 0600)
	}
	store2 := &store.Store{Root: a.Root}
	store2.SetActive(store.ProviderKimiCode, "", n.ID)
	rows, _ := a.List()
	for _, r := range rows {
		fmt.Println("row", r.ID, r.Active, r.Status)
	}
	m, err := a.Manager(n.ID, "")
	if err != nil {
		t.Fatalf("manager: %v", err)
	}
	pm := m.(privateAccountManager)
	inner, cerr := pm.Manager.Capture(context.Background(), "")
	t.Logf("inner capture=%d err=%v", len(inner), cerr)
	got, cerr := m.Capture(context.Background(), "")
	t.Logf("private capture=%d err=%v", len(got), cerr)
}
