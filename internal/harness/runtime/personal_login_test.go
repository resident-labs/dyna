package runtime

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestUsePersonalLoginCopiesOnce(t *testing.T) {
	r, home := ownerImportFixture(t)
	ownerImportSeed(t, home, "claude-code")
	n, err := r.UsePersonalLogin(context.Background(), "claude-code")
	if err != nil || n.Status != "ready" || !n.Active {
		t.Fatalf("got %+v, %v", n, err)
	}
	found, err := r.FindPersonalLogin(context.Background(), "claude-code")
	if err != nil || found == nil || !found.Saved {
		t.Fatalf("saved login not reported: %+v, %v", found, err)
	}
	if _, err := r.UsePersonalLogin(context.Background(), "claude-code"); err == nil {
		t.Fatal("a saved login was copied again")
	}
}

func TestFindPersonalLoginSkipsHarnessWithOtherAccount(t *testing.T) {
	r, home := ownerImportFixture(t)
	ownerImportSeed(t, home, "claude-code")
	if _, err := r.UsePersonalLogin(context.Background(), "claude-code"); err != nil {
		t.Fatal(err)
	}
	// The owner later signs in to Claude Code with another account.
	ownerImportWrite(t, filepath.Join(home, ".claude/.credentials.json"), `{"claudeAiOauth":{"accessToken":"other-access","refreshToken":"other-refresh","expiresAt":4102444800000,"scopes":["user:inference"]}}`)
	account, _ := json.Marshal(map[string]any{"oauthAccount": map[string]string{"accountUuid": "aaaaaaaa-bbbb-cccc-dddd-000000000009"}})
	ownerImportWrite(t, filepath.Join(home, ".claude.json"), string(account))
	if found, err := r.FindPersonalLogin(context.Background(), "claude-code"); err != nil || found != nil {
		t.Fatalf("offered a copy into a harness with another account: %+v, %v", found, err)
	}
}
