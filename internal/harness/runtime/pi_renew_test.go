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
	writePiGrants(t, account, map[string]piAuthFixture{"openai-codex": {Type: "oauth", Access: "stale-access", Refresh: "grant", Expires: time.Now().Add(-time.Hour).UnixMilli(), AccountID: "workspace"}})

	var gotHome, gotProvider, gotExecutable string
	previous := piRenewRunner
	t.Cleanup(func() { piRenewRunner = previous })
	piRenewRunner = func(_ context.Context, home, provider, executable string) error {
		gotHome, gotProvider, gotExecutable = home, provider, executable
		// Pi writes the renewed entry under its own lock; the runner stands in
		// for that write.
		writePiGrants(t, account, map[string]piAuthFixture{"openai-codex": {Type: "oauth", Access: "fresh-access", Refresh: "grant", Expires: time.Now().Add(time.Hour).UnixMilli(), AccountID: "workspace"}})
		return nil
	}

	m, _, err := accounts.scopedManager("pi", account.Home, "/fake/pi/bin/pi")
	if err != nil {
		t.Fatal(err)
	}
	login := piLoginOf(t, "stale-access")
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
	writePiGrants(t, account, map[string]piAuthFixture{"openai-codex": {Type: "oauth", Access: "stale-access", Refresh: "grant", Expires: time.Now().Add(-time.Hour).UnixMilli(), AccountID: "workspace"}})
	previous := piRenewRunner
	t.Cleanup(func() { piRenewRunner = previous })
	piRenewRunner = func(context.Context, string, string, string) error { return native.ErrInvalidGrant }

	m, _, err := accounts.scopedManager("pi", account.Home, "/fake/pi/bin/pi")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.(*nativeFilesManager).Refresh(context.Background(), piLoginOf(t, "stale-access")); err == nil {
		t.Fatal("a rejected grant was reported as renewed")
	}
}

// piAuthFixture is one provider entry of an account's auth.json.
type piAuthFixture struct {
	Type      string `json:"type"`
	Access    string `json:"access,omitempty"`
	Refresh   string `json:"refresh,omitempty"`
	Expires   int64  `json:"expires,omitempty"`
	AccountID string `json:"accountId,omitempty"`
	Key       string `json:"key,omitempty"`
}

// piLoginOf builds the saved login a capture returns for one account.
func piLoginOf(t *testing.T, access string) native.Captured {
	t.Helper()
	auth := map[string]piAuthFixture{"openai-codex": {
		Type: "oauth", Access: access, Refresh: "grant", Expires: time.Now().Add(-time.Hour).UnixMilli(), AccountID: "workspace",
	}}
	entry, err := json.Marshal(auth)
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
func writePiGrants(t *testing.T, account NativeAccount, grants map[string]piAuthFixture) {
	t.Helper()
	raw, err := json.Marshal(grants)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(account.Home, "auth.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
}

// The account-refresh call path hands the renewal the executable it was given,
// so a caller that resolved the Pi bundle (Voice does) renews in the account's
// own home.
func TestPiCredentialRefreshPassesTheExecutableAndHome(t *testing.T) {
	accounts := testAccounts(t)
	account, err := accounts.CreateProvider("pi", store.ProviderChatGPT, store.KindOAuth, "ChatGPT")
	if err != nil {
		t.Fatal(err)
	}
	writePiGrants(t, account, map[string]piAuthFixture{"openai-codex": {Type: "oauth", Access: "stale-access", Refresh: "grant", Expires: time.Now().Add(-time.Hour).UnixMilli(), AccountID: "workspace"}})
	captured, err := savePiLogin(t, accounts, account, "stale-access")
	if err != nil {
		t.Fatal(err)
	}
	if err = accounts.setActiveAccount(captured); err != nil {
		t.Fatal(err)
	}

	var gotHome, gotExecutable string
	previous := piRenewRunner
	t.Cleanup(func() { piRenewRunner = previous })
	piRenewRunner = func(_ context.Context, home, provider, executable string) error {
		gotHome, gotExecutable = home, executable
		writePiGrants(t, account, map[string]piAuthFixture{"openai-codex": {Type: "oauth", Access: "fresh-access", Refresh: "grant", Expires: time.Now().Add(time.Hour).UnixMilli(), AccountID: "workspace"}})
		return nil
	}
	result, err := accounts.RefreshCredentials(t.Context(), account.ID, "/bundle/bin/pi")
	if err != nil || !result.Refreshed {
		t.Fatalf("refresh = %+v, %v", result, err)
	}
	if gotHome != account.Home || gotExecutable != "/bundle/bin/pi" {
		t.Fatalf("renewal ran for home %q executable %q; want the account home and the given executable", gotHome, gotExecutable)
	}
}

// savePiLogin stores the login snapshot a refresh reads, and makes the account
// reference it.
func savePiLogin(t *testing.T, accounts *Accounts, account NativeAccount, access string) (NativeAccount, error) {
	t.Helper()
	m, _, err := accounts.scopedManager("pi", account.Home, "")
	if err != nil {
		return NativeAccount{}, err
	}
	logins, err := m.Capture(t.Context(), "")
	if err != nil || len(logins) == 0 {
		return NativeAccount{}, native.ErrNotSignedIn
	}
	if err = accounts.transaction(func(state *store.State) error {
		state.Find(account.ID).Identity = logins[0].Identity
		return writeAccountLogin(account.Home, logins[0])
	}, true); err != nil {
		return NativeAccount{}, err
	}
	return accounts.Find(account.ID)
}
