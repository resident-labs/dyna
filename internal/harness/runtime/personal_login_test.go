package runtime

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestFindPersonalLoginReadsWithoutCopying(t *testing.T) {
	r, home := ownerImportFixture(t)
	if found, err := r.FindPersonalLogin(context.Background(), "muse"); err != nil || found != nil {
		t.Fatalf("no owner login: got %+v, %v", found, err)
	}
	ownerImportSeed(t, home, "muse")
	found, err := r.FindPersonalLogin(context.Background(), "muse")
	if err != nil || found == nil || found.Harness != "muse" || found.Label != "owner@example.test" || found.Saved {
		t.Fatalf("got %+v, %v", found, err)
	}
	if _, err := r.Accounts.Active("muse"); err == nil {
		t.Fatal("the scan copied the login")
	}
}

func TestUsePersonalLoginCopiesOnce(t *testing.T) {
	r, home := ownerImportFixture(t)
	ownerImportSeed(t, home, "codex")
	n, err := r.UsePersonalLogin(context.Background(), "codex")
	if err != nil || n.Status != "ready" || !n.Active {
		t.Fatalf("got %+v, %v", n, err)
	}
	found, err := r.FindPersonalLogin(context.Background(), "codex")
	if err != nil || found == nil || !found.Saved {
		t.Fatalf("saved login not reported: %+v, %v", found, err)
	}
	if _, err := r.UsePersonalLogin(context.Background(), "codex"); err == nil {
		t.Fatal("a saved login was copied again")
	}
}

func TestFindPersonalLoginSkipsHarnessWithOtherAccount(t *testing.T) {
	r, home := ownerImportFixture(t)
	ownerImportSeed(t, home, "codex")
	if _, err := r.UsePersonalLogin(context.Background(), "codex"); err != nil {
		t.Fatal(err)
	}
	// The owner later signs in to Codex with another account.
	jwt := "e30." + base64.RawURLEncoding.EncodeToString([]byte(`{"https://api.openai.com/auth":{"chatgpt_user_id":"other","chatgpt_account_id":"elsewhere"}}`)) + ".signature"
	raw, _ := json.Marshal(map[string]any{"auth_mode": "chatgpt", "tokens": map[string]string{"id_token": jwt, "access_token": "other-access", "refresh_token": "other-refresh", "account_id": "elsewhere"}})
	ownerImportWrite(t, filepath.Join(home, ".codex/auth.json"), string(raw))
	if found, err := r.FindPersonalLogin(context.Background(), "codex"); err != nil || found != nil {
		t.Fatalf("offered a copy into a harness with another account: %+v, %v", found, err)
	}
}
