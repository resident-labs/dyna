package runtime

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"dyna-agent/internal/account/store"
	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	state "dyna-agent/internal/state"
)

// Pi names an OAuth provider differently from its API-key provider for ChatGPT.
func TestPiOAuthProviderIDMapsTheSignInNames(t *testing.T) {
	want := map[store.Provider]string{
		store.ProviderAnthropic: "anthropic",
		store.ProviderChatGPT:   "openai-codex",
		store.ProviderKimiCode:  "kimi-coding",
		store.ProviderXAI:       "xai",
		store.ProviderMetaMuse:  "meta",
	}
	for provider, id := range want {
		if got, ok := PiOAuthProviderID(provider); !ok || got != id {
			t.Fatalf("PiOAuthProviderID(%q) = %q, %v; want %q", provider, got, ok, id)
		}
	}
	for _, provider := range []store.Provider{store.ProviderDeepSeek, store.ProviderZAI, store.ProviderOpenCodeGo} {
		if _, ok := PiOAuthProviderID(provider); ok {
			t.Fatalf("PiOAuthProviderID(%q) accepted a provider without Pi OAuth", provider)
		}
	}
}

// Only Pi picks its provider from the model. Another harness keeps its own
// selection, because its provider keys live in its own store, not in accounts.
func TestProviderForModelKeepsOtherHarnessesOnTheirProvider(t *testing.T) {
	for _, c := range []struct {
		harness, model string
		want           store.Provider
	}{
		{"pi", "zai/glm-5.3", store.ProviderZAI},
		{"pi", "kimi-coding/k3.1-pro", store.ProviderKimiCode},
		{"pi", "xai/grok-4.7", store.ProviderXAI},
		{"pi", "deepseek/deepseek-flash", store.ProviderDeepSeek},
		{"opencode", "zai/glm-5.3", store.ProviderOpenCodeGo},
		{"opencode", "opencode-go/mimo-v2.6-pro", store.ProviderOpenCodeGo},
		{"claude-code", "haiku", store.ProviderAnthropic},
	} {
		if got := providerForModel(c.harness, c.model); got != c.want {
			t.Fatalf("%s %s: provider %q, want %q", c.harness, c.model, got, c.want)
		}
	}
}

// An OpenCode account whose credential names another provider stays the one a
// zai model uses, because OpenCode serves one selection and takes the key from
// its own store.
func TestOpenCodeSelectionServesAnyKeyedAccount(t *testing.T) {
	a := testAccounts(t)
	keyed, err := a.Create("opencode", "zai-key")
	if err != nil {
		t.Fatal(err)
	}
	// An OpenCode integration key names its own provider on the account row.
	if err = a.transaction(func(s *store.State) error {
		s.Find(keyed.ID).Provider = store.ProviderZAI
		return nil
	}, true); err != nil {
		t.Fatal(err)
	}
	if err := a.setActive("opencode", keyed.ID); err != nil {
		t.Fatal(err)
	}
	got, err := a.activeAccount("opencode", providerForModel("opencode", "zai/glm-5.3"), "")
	if err != nil || got.ID != keyed.ID {
		t.Fatalf("opencode selection = %+v %v", got, err)
	}
}

// Two harnesses that both run anthropic keep their own account: choosing one
// never takes the other's account away, because their credential formats cannot
// be exchanged yet.
func TestProviderSelectionDoesNotTakeAnotherHarnessesAccount(t *testing.T) {
	a := testAccounts(t)
	work := signedClaude(t, a, "work", "aaaaaaaa-bbbb-cccc-dddd-000000000009")
	personal := signedClaude(t, a, "personal", "aaaaaaaa-bbbb-cccc-dddd-00000000000a")
	pi := signedPiAccount(t, &Runtime{Accounts: a}, store.ProviderAnthropic, "pi-anthropic")
	if err := a.setActiveAccount(pi); err != nil {
		t.Fatal(err)
	}
	if got, err := a.activeAccount("pi", store.ProviderAnthropic, ""); err != nil || got.ID != pi.ID {
		t.Fatalf("pi took %+v (%v), want its own account", got, err)
	}
	// Claude Code has no selection yet, so Pi's choice is not its own.
	if _, err := a.activeAccount("claude-code", store.ProviderAnthropic, ""); !errors.Is(err, errAccountMissing) {
		t.Fatalf("claude code inherited Pi's selection: %v", err)
	}
	if err := a.setActiveAccount(work); err != nil {
		t.Fatal(err)
	}
	if got, err := a.activeAccount("claude-code", store.ProviderAnthropic, ""); err != nil || got.ID != work.ID {
		t.Fatalf("claude code took %+v (%v), want the account it selected", got, err)
	}
	// Choosing Pi keeps Claude Code's own choice, not just any account of its own.
	if err := a.setActiveAccount(pi); err != nil {
		t.Fatal(err)
	}
	if got, err := a.activeAccount("claude-code", store.ProviderAnthropic, ""); err != nil || got.ID != work.ID {
		t.Fatalf("choosing Pi moved Claude Code to %+v (%v)", got, err)
	}
	if got, err := a.activeAccount("pi", store.ProviderAnthropic, ""); err != nil || got.ID != pi.ID {
		t.Fatalf("pi lost its account after Claude Code chose one: %+v %v", got, err)
	}
	// Claude Code can still change its own choice, and Pi keeps its own.
	if err := a.setActiveAccount(personal); err != nil {
		t.Fatal(err)
	}
	if got, err := a.activeAccount("claude-code", store.ProviderAnthropic, ""); err != nil || got.ID != personal.ID {
		t.Fatalf("claude code took %+v (%v), want the account it selected last", got, err)
	}
	if got, err := a.activeAccount("pi", store.ProviderAnthropic, ""); err != nil || got.ID != pi.ID {
		t.Fatalf("pi took %+v (%v) after Claude Code chose again", got, err)
	}
}

// An OpenCode account whose credential names another provider admits with the
// provider the harness selects, because the selection row and the account row
// keep their own values.
func TestOpenCodeKeyedAccountAdmits(t *testing.T) {
	a := testAccounts(t)
	n, err := a.Create("opencode", "zai-key")
	if err != nil {
		t.Fatal(err)
	}
	captured := fixtureLogin("opencode-keyed")
	if err = writeAccountLogin(n.Home, captured); err != nil {
		t.Fatal(err)
	}
	if err = a.transaction(func(s *store.State) error {
		e := s.Find(n.ID)
		e.Provider, e.Identity, e.NeedsLogin = store.ProviderZAI, captured.Identity, false
		return nil
	}, true); err != nil {
		t.Fatal(err)
	}
	if n, err = a.Find(n.ID); err != nil || n.Status != "ready" {
		t.Fatalf("keyed account: %+v %v", n, err)
	}
	if err = a.setActiveAccount(n); err != nil {
		t.Fatal(err)
	}
	expected, err := a.CheckPreparedAccount("opencode", n.ID, "", "")
	if err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(filepath.Dir(a.Root))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err = state.For(db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
		return a.RecheckPreparedAccount(ctx, conn, expected, "")
	}); err != nil {
		t.Fatalf("the keyed OpenCode account did not admit: %v", err)
	}
}

// A Pi account selected for its provider passes admission, also through a room
// selection, and a room selection that moves to another account fails it.
func TestPiProviderSelectionAdmits(t *testing.T) {
	a := testAccounts(t)
	first := signedPiAccount(t, &Runtime{Accounts: a}, store.ProviderZAI, "first")
	second := signedPiAccount(t, &Runtime{Accounts: a}, store.ProviderZAI, "second")
	if err := a.setActiveAccount(first); err != nil {
		t.Fatal(err)
	}
	expected, err := a.CheckPreparedAccount("pi", first.ID, "room-1", "")
	if err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(filepath.Dir(a.Root))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err = state.For(db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
		if err := a.RecheckPreparedAccount(ctx, conn, expected, ""); err != nil {
			t.Fatalf("the prepared Pi account did not admit: %v", err)
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO account_active(provider, harness, scope, account) VALUES (?,?,?,?)
ON CONFLICT(provider, harness, scope) DO UPDATE SET account = excluded.account`, store.ProviderZAI, "pi", "room-1", second.ID); err != nil {
			return err
		}
		if err := a.RecheckPreparedAccount(ctx, conn, expected, ""); err == nil {
			t.Fatal("admitted after the room selection moved to another account")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
