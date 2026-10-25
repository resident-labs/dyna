package runtime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"dyna-agent/internal/account/native"
	"dyna-agent/internal/account/store"
)

// Dyna renews a Pi grant by asking Pi to do it, then reads the account's own
// auth.json again. It never runs a provider refresh itself.
func TestPiRenewalAsksPiAndRereadsTheAccountLogin(t *testing.T) {
	accounts := testAccounts(t)
	account, err := accounts.CreateProvider("pi", store.ProviderChatGPT, store.KindOAuth, "ChatGPT")
	if err != nil {
		t.Fatal(err)
	}
	writePiGrants(t, account, map[string]string{"openai-codex": "stale-access"})

	var gotHome, gotProvider, gotExecutable string
	previous := piRenewRunner
	t.Cleanup(func() { piRenewRunner = previous })
	piRenewRunner = func(_ context.Context, home, provider, executable string) error {
		gotHome, gotProvider, gotExecutable = home, provider, executable
		// Pi writes the renewed entry under its own lock; the runner stands in
		// for that write.
		writePiGrants(t, account, map[string]string{"openai-codex": "fresh-access"})
		return nil
	}

	m, _, err := accounts.scopedManager("pi", account.Home, "/fake/pi/bin/pi")
	if err != nil {
		t.Fatal(err)
	}
	login := piLoginOf(t, account, "stale-access")
	if id, _, _, _, grantErr := native.PiGrant(login.Credential); grantErr != nil || id != "openai-codex" {
		t.Fatalf("fixture grant = %q, %v", id, grantErr)
	}
	renewed, err := m.(*nativeFilesManager).Refresh(context.Background(), login)
	if err != nil {
		t.Fatal(err)
	}
	if gotHome != account.Home || gotProvider != "openai-codex" || gotExecutable != "/fake/pi/bin/pi" {
		t.Fatalf("renewal ran for home %q provider %q executable %q", gotHome, gotProvider, gotExecutable)
	}
	if _, access, _, _, err := native.PiGrant(renewed.Credential); err != nil || access != "fresh-access" {
		t.Fatalf("renewed access = %q, %v", access, err)
	}
}

// A provider that rejects the grant asks for a sign-in; nothing else does.
func TestPiRenewalReportsARejectedGrant(t *testing.T) {
	accounts := testAccounts(t)
	account, err := accounts.CreateProvider("pi", store.ProviderChatGPT, store.KindOAuth, "ChatGPT")
	if err != nil {
		t.Fatal(err)
	}
	writePiGrants(t, account, map[string]string{"openai-codex": "stale-access"})
	previous := piRenewRunner
	t.Cleanup(func() { piRenewRunner = previous })
	piRenewRunner = func(context.Context, string, string, string) error { return native.ErrInvalidGrant }

	m, _, err := accounts.scopedManager("pi", account.Home, "/fake/pi/bin/pi")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.(*nativeFilesManager).Refresh(context.Background(), piLoginOf(t, account, "stale-access")); err == nil {
		t.Fatal("a rejected grant was reported as renewed")
	}
}

// piLoginOf builds the saved login a capture returns for one account.
func piLoginOf(t *testing.T, account NativeAccount, access string) native.Captured {
	t.Helper()
	entry, err := json.Marshal(map[string]any{"openai-codex": map[string]any{
		"type": "oauth", "access": access, "refresh": "grant", "expires": time.Now().Add(-time.Hour).UnixMilli(), "accountId": "workspace",
	}})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]json.RawMessage{"auth.json": json.RawMessage(entry)})
	if err != nil {
		t.Fatal(err)
	}
	return native.Captured{Identity: "pi:openai-codex", Credential: store.Credential{Schema: "pi/native-files@1", Payload: payload}}
}

// writePiGrants writes the account's auth.json entry map.
func writePiGrants(t *testing.T, account NativeAccount, access map[string]string) {
	t.Helper()
	entries := map[string]any{}
	for id, value := range access {
		entries[id] = map[string]any{"type": "oauth", "access": value, "refresh": "grant", "expires": time.Now().Add(-time.Hour).UnixMilli(), "accountId": "workspace"}
	}
	raw, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(account.Home, "auth.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
}
