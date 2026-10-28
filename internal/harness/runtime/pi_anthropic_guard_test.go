package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	accountstore "dyna-agent/internal/account/store"
	harnessmgr "dyna-agent/internal/harness/config"
	"dyna-agent/internal/profile"
)

// A Pi profile on an anthropic model must not run a Claude Pro/Max OAuth
// sign-in: Anthropic bills that as extra usage through a third-party harness.
func TestPiProfileRefusesAnthropicOAuth(t *testing.T) {
	accounts := testAccounts(t)
	account, err := accounts.CreateProvider("pi", accountstore.ProviderAnthropic, accountstore.KindOAuth, "claude")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(account.Home, "auth.json"), []byte(`{"anthropic":{"type":"oauth","access":"fixture-access","refresh":"fixture-refresh","expires":4102444800}}`), 0600); err != nil {
		t.Fatal(err)
	}
	captured, err := accounts.Capture(context.Background(), account.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := accounts.selectPinned(captured); err != nil {
		t.Fatal(err)
	}
	bundles := manager(t)
	install(t, bundles, "pi", "1.3.0-dyna.5")
	r := &Runtime{Bundles: bundles, Accounts: accounts, Config: harnessmgr.NewConfigManager(), Generations: filepath.Join(t.TempDir(), "generations")}
	p := profile.Profile{Name: "pi-claude", Harness: "pi", Model: "anthropic/claude-fable-5", RuntimeVersion: piForkVersion}
	if _, _, err := r.Prepare(context.Background(), p, nil); !errors.Is(err, ErrPiAnthropicOAuth) {
		t.Fatalf("Prepare = %v, want the Anthropic OAuth refusal", err)
	}
	if d := r.Doctor(context.Background(), p); d.Status != "blocked" || !strings.Contains(d.Reason, "Claude Code") {
		t.Fatalf("Doctor = %+v, want the Claude Code advice", d)
	}
}

// The Pi sign-in button must not start a Claude Pro/Max OAuth flow, even if a
// caller asks for the anthropic provider directly.
func TestPiSignInRefusesAnthropicOAuth(t *testing.T) {
	r := providerKeysRuntime(t)
	bundle := install(t, r.Bundles, "pi", "1.3.0-dyna.5")
	if _, err := r.beginPiProviderSignIn(bundle.Executable, "", accountstore.ProviderAnthropic); !errors.Is(err, ErrPiAnthropicOAuth) {
		t.Fatalf("Pi anthropic sign-in = %v, want the Anthropic OAuth refusal", err)
	}
}

// An anthropic OAuth entry that already exists in a Pi auth.json is never used
// for a turn. An Anthropic API key is the one allowed exception.
func TestPiAnthropicOAuthEntryNeverRunsATurn(t *testing.T) {
	accounts := testAccounts(t)
	oauth, err := accounts.CreateProvider("pi", accountstore.ProviderAnthropic, accountstore.KindOAuth, "claude")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oauth.Home, "auth.json"), []byte(`{"anthropic":{"type":"oauth","access":"fixture-access","refresh":"fixture-refresh","expires":4102444800}}`), 0600); err != nil {
		t.Fatal(err)
	}
	captured, err := accounts.Capture(context.Background(), oauth.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if accountRuns("pi", captured, accountstore.ProviderAnthropic) {
		t.Fatal("a Pi anthropic OAuth entry can run a turn")
	}

	key, err := accounts.CreateProvider("pi", accountstore.ProviderAnthropic, accountstore.KindAPIKey, "claude-key")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(key.Home, "auth.json"), []byte(`{"anthropic":{"type":"api_key","key":"fixture-key"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	keyAccount, err := accounts.Capture(context.Background(), key.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if !accountRuns("pi", keyAccount, accountstore.ProviderAnthropic) {
		t.Fatal("an Anthropic API key cannot run a Pi anthropic turn")
	}
}
