package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"dyna-agent/internal/account/native"
	"dyna-agent/internal/account/store"
)

// A running Claude Code turn rotates the grant in the account's own folder, so a
// search reads the rotated token rather than a saved snapshot that is behind it.
func TestSavedCredentialReadsTheRotatedGrant(t *testing.T) {
	accounts := testAccounts(t)
	account, err := accounts.Create("claude", "work")
	if err != nil {
		t.Fatal(err)
	}
	writeAccountFixture(t, account)
	account, err = accounts.Capture(context.Background(), account.ID, "/fake/native")
	if err != nil {
		t.Fatal(err)
	}
	if err = accounts.setActiveAccount(account); err != nil {
		t.Fatal(err)
	}
	rotated := `{"claudeAiOauth":{"accessToken":"rotated-access","refreshToken":"rotated-refresh","expiresAt":1999999999999,"scopes":["user:inference"]}}`
	if err = os.WriteFile(filepath.Join(account.Home, ".credentials.json"), []byte(rotated), 0600); err != nil {
		t.Fatal(err)
	}
	credential, err := accounts.SavedCredential(t.Context(), store.ProviderAnthropic, "", "")
	if err != nil {
		t.Fatal(err)
	}
	token, err := native.ClaudeAccessToken(credential)
	if err != nil {
		t.Fatal(err)
	}
	if token != "rotated-access" {
		t.Fatalf("token = %q, want the grant Claude Code rotated in its own folder", token)
	}
}

// The web search tool reads a Claude Code grant from the active account of its
// provider, and never reads another harness's account or a provider the owner
// did not add.
func TestSavedCredentialReadsTheActiveClaudeCodeGrant(t *testing.T) {
	accounts := testAccounts(t)
	account, err := accounts.Create("claude", "work")
	if err != nil {
		t.Fatal(err)
	}
	writeAccountFixture(t, account)
	account, err = accounts.Capture(context.Background(), account.ID, "/fake/native")
	if err != nil {
		t.Fatal(err)
	}
	if err = accounts.setActiveAccount(account); err != nil {
		t.Fatal(err)
	}
	credential, err := accounts.SavedCredential(t.Context(), store.ProviderAnthropic, "", "")
	if err != nil {
		t.Fatal(err)
	}
	token, err := native.ClaudeAccessToken(credential)
	if err != nil {
		t.Fatal(err)
	}
	if token != "native-access-canary" {
		t.Fatalf("token = %q, want the saved Claude Code grant", token)
	}
	if err := accounts.SetRepos(account.ID, []string{"github.com/acme/work"}); err != nil {
		t.Fatal(err)
	}
	if _, err := accounts.SavedCredential(t.Context(), store.ProviderAnthropic, "", "github.com/acme/personal"); err == nil {
		t.Fatal("another repository read the grant, want the scope refused")
	}
	if _, err := accounts.SavedCredential(t.Context(), store.ProviderXAI, "", ""); !errors.Is(err, native.ErrNotSignedIn) {
		t.Fatalf("another provider: err = %v, want ErrNotSignedIn", err)
	}
}
