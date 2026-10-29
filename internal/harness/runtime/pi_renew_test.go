package runtime

import (
	"context"
	"encoding/json"
	"errors"
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

// The renewal runner passes the native command its provider and private home.
func TestPiRenewRunsTheNativeCommand(t *testing.T) {
	home := t.TempDir()
	executable := filepath.Join(home, "pi")
	script := "#!/bin/sh\nprintf '%s\\n%s' \"$*\" \"$PI_CODING_AGENT_DIR\" > \"$PI_CODING_AGENT_DIR/invocation\"\nprintf '{\"status\":\"no_credential\"}\\n'\nexit 3\n"
	if err := os.WriteFile(executable, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	if err := runPiRenew(t.Context(), home, "openai-codex", executable); !errors.Is(err, native.ErrNotSignedIn) {
		t.Fatalf("error = %v, want ErrNotSignedIn", err)
	}
	raw, err := os.ReadFile(filepath.Join(home, "invocation"))
	if err != nil {
		t.Fatal(err)
	}
	want := "auth refresh --provider openai-codex --json\n" + home
	if string(raw) != want {
		t.Fatalf("invocation = %q, want %q", raw, want)
	}
}

// Pi reports a typed result. Only invalid_grant asks for a new sign-in;
// missing credentials and every transient failure keep their existing meaning.
func TestPiRenewalResultClassification(t *testing.T) {
	for _, tc := range []struct {
		name   string
		output string
		want   string
	}{
		{"renewed", `{"status":"renewed"}`, "success"},
		{"no credential", `{"status":"no_credential"}`, "missing"},
		{"rejected grant", `{"status":"failed","kind":"invalid_grant","httpStatus":400}`, "invalid"},
		{"network", `{"status":"failed","kind":"network","httpStatus":0}`, "transient"},
		{"timeout", `{"status":"failed","kind":"timeout","httpStatus":0}`, "transient"},
		{"unknown", `{"status":"failed","kind":"unknown","httpStatus":500}`, "transient"},
		{"malformed", "{broken", "transient"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := classifyPiRenewResult([]byte(tc.output))
			switch tc.want {
			case "success":
				if err != nil {
					t.Fatalf("renewal failed: %v", err)
				}
			case "missing":
				if !errors.Is(err, native.ErrNotSignedIn) {
					t.Fatalf("error = %v, want ErrNotSignedIn", err)
				}
			case "invalid":
				if !errors.Is(err, native.ErrInvalidGrant) {
					t.Fatalf("error = %v, want ErrInvalidGrant", err)
				}
			default:
				if err == nil || errors.Is(err, native.ErrInvalidGrant) || errors.Is(err, native.ErrNotSignedIn) {
					t.Fatalf("error = %v, want a transient renewal failure", err)
				}
			}
		})
	}
}

// Every renewal failure records a reason that names the exact error class, so
// a stored live record shows which path failed instead of one shared label.
func TestRenewalOutcomeNamesTheExactError(t *testing.T) {
	for _, tc := range []struct {
		name       string
		err        error
		wantResult store.RenewalResult
		wantReason string
	}{
		{"rejected grant", native.ErrInvalidGrant, store.RenewalResultInvalidGrant, "invalid_grant"},
		{"no sign-in", native.ErrNotSignedIn, store.RenewalResultNoCredential, "not signed in"},
		{"changed identity", native.ErrNoIdentity, store.RenewalResultNoCredential, "identity mismatch"},
		{"unsupported", native.ErrUnsupported, store.RenewalResultNoCredential, "unsupported"},
		{"missing login file", os.ErrNotExist, store.RenewalResultNoCredential, "login file missing"},
		{"canceled", context.Canceled, store.RenewalResultTransient, "canceled"},
		{"other failure", errors.New("boom"), store.RenewalResultTransient, "transient"},
		{"renewed", nil, store.RenewalResultRenewed, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := CredentialRefreshResult{Refreshed: tc.err == nil}
			gotResult, gotReason := renewalOutcome(result, tc.err)
			if gotResult != tc.wantResult || gotReason != tc.wantReason {
				t.Fatalf("outcome = %q, %q; want %q, %q", gotResult, gotReason, tc.wantResult, tc.wantReason)
			}
		})
	}
}

// A Pi manager whose host carries no agent dir cannot locate the account home;
// the recorded renewal names that gap instead of hiding it.
func TestPiRenewalWithoutTheAgentHomeReportsUnsupported(t *testing.T) {
	m, ok := NativeRuntimeManager("pi", native.Host{Env: func(string) string { return "" }})
	if !ok {
		t.Fatal("pi has a native manager")
	}
	_, err := m.(*nativeFilesManager).root("")
	if !errors.Is(err, native.ErrUnsupported) {
		t.Fatalf("error = %v, want ErrUnsupported", err)
	}
	if _, reason := renewalOutcome(CredentialRefreshResult{}, err); reason != "unsupported" {
		t.Fatalf("reason = %q, want the unsupported gap named", reason)
	}
}

// A renewal that lands under a different provider id is an identity mismatch,
// never a silent success.
func TestPiRenewalWithAChangedProviderReportsIdentityMismatch(t *testing.T) {
	accounts := testAccounts(t)
	account, err := accounts.CreateProvider("pi", store.ProviderChatGPT, store.KindOAuth, "ChatGPT")
	if err != nil {
		t.Fatal(err)
	}
	writePiGrants(t, account, map[string]piAuthFixture{"openai-codex": {Type: "oauth", Access: "stale-access", Refresh: "grant", Expires: time.Now().Add(-time.Hour).UnixMilli(), AccountID: "workspace"}})
	previous := piRenewRunner
	t.Cleanup(func() { piRenewRunner = previous })
	piRenewRunner = func(_ context.Context, _, _, _ string) error {
		writePiGrants(t, account, map[string]piAuthFixture{"another-provider": {Type: "oauth", Access: "fresh-access", Refresh: "grant", Expires: time.Now().Add(time.Hour).UnixMilli(), AccountID: "workspace"}})
		return nil
	}
	m, _, err := accounts.scopedManager("pi", account.Home, "/fake/pi/bin/pi")
	if err != nil {
		t.Fatal(err)
	}
	_, err = m.(*nativeFilesManager).Refresh(context.Background(), piLoginOf(t, "stale-access"))
	if !errors.Is(err, native.ErrNoIdentity) {
		t.Fatalf("error = %v, want ErrNoIdentity", err)
	}
	if _, reason := renewalOutcome(CredentialRefreshResult{}, err); reason != "identity mismatch" {
		t.Fatalf("reason = %q, want the identity mismatch named", reason)
	}
}

// A renewal whose login file disappeared is reported as a missing file, with
// the account untouched.
func TestPiRenewalWithoutALoginFileReportsItMissing(t *testing.T) {
	accounts := testAccounts(t)
	account, err := accounts.CreateProvider("pi", store.ProviderChatGPT, store.KindOAuth, "ChatGPT")
	if err != nil {
		t.Fatal(err)
	}
	writePiGrants(t, account, map[string]piAuthFixture{"openai-codex": {Type: "oauth", Access: "stale-access", Refresh: "grant", Expires: time.Now().Add(-time.Hour).UnixMilli(), AccountID: "workspace"}})
	if err = os.Remove(filepath.Join(account.Home, "auth.json")); err != nil {
		t.Fatal(err)
	}
	previous := piRenewRunner
	t.Cleanup(func() { piRenewRunner = previous })
	piRenewRunner = func(context.Context, string, string, string) error { return nil }
	m, _, err := accounts.scopedManager("pi", account.Home, "/fake/pi/bin/pi")
	if err != nil {
		t.Fatal(err)
	}
	_, err = m.(*nativeFilesManager).Refresh(context.Background(), piLoginOf(t, "stale-access"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("error = %v, want the missing login file", err)
	}
	if _, reason := renewalOutcome(CredentialRefreshResult{}, err); reason != "login file missing" {
		t.Fatalf("reason = %q, want the missing login file named", reason)
	}
}

// A successful renewal must update the stored credential too, so no later
// activation can restore a refresh token the provider already replaced.
func TestPiRenewalSavesTheRenewedCredentialInTheStore(t *testing.T) {
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
	// The store starts from the sign-in day credential, as a live account does.
	if err = accounts.transaction(func(state *store.State) error {
		state.Find(account.ID).Credential = piLoginOf(t, "stale-access").Credential
		return nil
	}, true); err != nil {
		t.Fatal(err)
	}
	previous := piRenewRunner
	t.Cleanup(func() { piRenewRunner = previous })
	piRenewRunner = func(_ context.Context, _, _, _ string) error {
		writePiGrants(t, account, map[string]piAuthFixture{"openai-codex": {Type: "oauth", Access: "fresh-access", Refresh: "grant2", Expires: time.Now().Add(time.Hour).UnixMilli(), AccountID: "workspace"}})
		return nil
	}
	if _, err = accounts.RefreshCredentials(t.Context(), account.ID, "/bundle/bin/pi"); err != nil {
		t.Fatal(err)
	}
	var access string
	if err = accounts.transaction(func(state *store.State) error {
		_, got, _, _, grantErr := native.PiGrant(state.Find(account.ID).Credential)
		if grantErr != nil {
			return grantErr
		}
		access = got
		return nil
	}, true); err != nil {
		t.Fatal(err)
	}
	if access != "fresh-access" {
		t.Fatalf("stored access = %q, want the renewed credential saved in the store", access)
	}
}

// A valid OAuth login that Pi reports as no_credential must not be recorded as
// a failed renewal: Pi's refresh command checks auth.apiKey, which an OAuth
// provider never has, so a healthy sign-in looks like a missing one.
func TestPiRenewalKeepsALoginPiStillHas(t *testing.T) {
	accounts := testAccounts(t)
	account, err := accounts.CreateProvider("pi", store.ProviderChatGPT, store.KindOAuth, "ChatGPT")
	if err != nil {
		t.Fatal(err)
	}
	writePiGrants(t, account, map[string]piAuthFixture{"openai-codex": {Type: "oauth", Access: "valid-access", Refresh: "grant", Expires: time.Now().Add(10 * time.Minute).UnixMilli(), AccountID: "workspace"}})
	previous := piRenewRunner
	t.Cleanup(func() { piRenewRunner = previous })
	piRenewRunner = func(_ context.Context, _, _, _ string) error { return native.ErrNotSignedIn }

	m, _, err := accounts.scopedManager("pi", account.Home, "/fake/pi/bin/pi")
	if err != nil {
		t.Fatal(err)
	}
	renewed, err := m.(*nativeFilesManager).Refresh(context.Background(), piLoginOf(t, "valid-access"))
	if err != nil {
		t.Fatalf("a login Pi still holds failed its renewal: %v", err)
	}
	if _, access, _, _, err := native.PiGrant(renewed.Credential); err != nil || access != "valid-access" {
		t.Fatalf("renewed access = %q, %v", access, err)
	}
}
