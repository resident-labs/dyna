package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"dyna-agent/internal/account/native"
	"dyna-agent/internal/account/store"
)

// seedPiLogin saves one provider login in a Pi account of its own and makes the
// account active, as a finished sign-in does.
func seedPiLogin(t *testing.T, accounts *Accounts, piID, entry string, kind store.Kind) NativeAccount {
	t.Helper()
	installFakePiAuth(t, accounts)
	account, err := accounts.CreateProvider("pi", piProvider(piID), kind, piID+" login")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(account.Home, "auth.json"), []byte(`{"`+piID+`":`+entry+`}`), 0600); err != nil {
		t.Fatal(err)
	}
	account, err = accounts.Capture(context.Background(), account.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if err = accounts.setActiveAccount(account); err != nil {
		t.Fatal(err)
	}
	return account
}

// futureGrant is a Pi ChatGPT login entry whose token lives for an hour, so
// reading it needs no renewal.
func futureGrant(access, account string) string {
	return fmt.Sprintf(`{"type":"oauth","access":%q,"refresh":"refresh-token","expires":%d,"accountId":%q}`, access, time.Now().Add(time.Hour).UnixMilli(), account)
}

// The one ChatGPT reader returns the typed grant of the active sign-in, and it
// renews nothing while the grant is fresh.
func TestChatGPTGrantReadsAFreshSignIn(t *testing.T) {
	accounts := testAccounts(t)
	seedPiLogin(t, accounts, "openai-codex", futureGrant("access-token", "workspace"), store.KindOAuth)
	// The read runs on the seed's fake auth runner; a renewal would take this
	// runner, so a fresh grant must never reach it.
	previous := piRenewRunner
	t.Cleanup(func() { piRenewRunner = previous })
	piRenewRunner = func(context.Context, string, string, string) error {
		t.Error("a fresh grant was renewed")
		return errors.New("a fresh grant was renewed")
	}
	grant, err := accounts.ChatGPTGrant(t.Context(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	if grant.Access != "access-token" || grant.Account != "workspace" {
		t.Fatalf("grant = %+v, want the saved ChatGPT sign-in", grant)
	}
}

func TestChatGPTGrantWithoutASignInIsNotSignedIn(t *testing.T) {
	accounts := testAccounts(t)
	if _, err := accounts.ChatGPTGrant(t.Context(), "", ""); !errors.Is(err, native.ErrNotSignedIn) {
		t.Fatalf("err = %v, want ErrNotSignedIn", err)
	}
}

// A grant that expires inside the renewal window is renewed before it is used,
// so a caller never receives a token that dies mid-request. A renewal that
// cannot run is a typed failure, never a stale token.
func TestChatGPTGrantRenewsADueSignIn(t *testing.T) {
	accounts := testAccounts(t)
	seedPiLogin(t, accounts, "openai-codex", `{"type":"oauth","access":"old-token","refresh":"refresh-token","expires":1,"accountId":"workspace"}`, store.KindOAuth)
	previous := piRenewRunner
	t.Cleanup(func() { piRenewRunner = previous })
	piRenewRunner = func(context.Context, string, string, string) error { return errors.New("the renewal failed") }
	if _, err := accounts.ChatGPTGrant(t.Context(), "", ""); !errors.Is(err, ErrGrantExpired) {
		t.Fatalf("err = %v, want the due grant reported as expired when its renewal fails", err)
	}
}

// A grant read is a plain read of the login file, so it works wherever Pi is
// not installed. Only a due grant's renewal runs Pi, so a missing Pi copy is
// a typed renewal failure, never a stale token.
func TestChatGPTGrantWithoutPiIsTyped(t *testing.T) {
	accounts := testAccounts(t)
	seedPiLogin(t, accounts, "openai-codex", `{"type":"oauth","access":"old-token","refresh":"refresh-token","expires":1,"accountId":"workspace"}`, store.KindOAuth)
	// Set after the seed, whose fake auth runner names its own Pi copy.
	accounts.PiExecutable = func() (string, error) { return "", ErrPiMissing }
	_, err := accounts.ChatGPTGrant(t.Context(), "", "")
	if !errors.Is(err, ErrGrantExpired) || !errors.Is(err, ErrPiMissing) {
		t.Fatalf("err = %v, want the due grant expired because its renewal could not run", err)
	}
}

// A caller passes its repository, and an account the owner limited to another
// repository never answers.
func TestChatGPTGrantKeepsTheAccountsRepositoryScope(t *testing.T) {
	accounts := testAccounts(t)
	limited := seedPiLogin(t, accounts, "openai-codex", futureGrant("work-token", "work"), store.KindOAuth)
	if err := accounts.SetRepos(limited.ID, []string{"github.com/acme/work"}); err != nil {
		t.Fatal(err)
	}
	grant, err := accounts.ChatGPTGrant(t.Context(), "", "github.com/acme/work")
	if err != nil {
		t.Fatal(err)
	}
	if grant.Access != "work-token" {
		t.Fatalf("grant = %+v, want the account the repository allows", grant)
	}
	// The same account must not answer for another repository.
	if refused, err := accounts.ChatGPTGrant(t.Context(), "", "github.com/acme/personal"); err == nil {
		t.Fatalf("another repository read %q, want the scope refused", refused.Access)
	}
	// A room may select another account of the same provider, and that account's
	// own scope decides, while the global account keeps refusing that repository.
	room := seedPiLogin(t, accounts, "openai-codex", futureGrant("personal-token", "personal"), store.KindOAuth)
	if err := accounts.SetRepos(room.ID, []string{"github.com/acme/personal"}); err != nil {
		t.Fatal(err)
	}
	if err := accounts.setActiveAccount(limited); err != nil {
		t.Fatal(err)
	}
	if err := accounts.SelectInScope(room, "room-1"); err != nil {
		t.Fatal(err)
	}
	grant, err = accounts.ChatGPTGrant(t.Context(), "room-1", "github.com/acme/personal")
	if err != nil {
		t.Fatal(err)
	}
	if grant.Access != "personal-token" {
		t.Fatalf("room selection read %q, want the account the room selected", grant.Access)
	}
	if refused, err := accounts.ChatGPTGrant(t.Context(), "", "github.com/acme/personal"); err == nil {
		t.Fatalf("the global account read %q, want its scope to refuse the repository", refused.Access)
	}
}

// A Muse OAuth sign-in keeps its minted inference key in access, so the key
// reader takes it from there instead of refusing a supported sign-in.
func TestPiInferenceKeyReadsAnOAuthSignIn(t *testing.T) {
	accounts := testAccounts(t)
	seedPiLogin(t, accounts, "meta", `{"type":"oauth","access":"minted-key","refresh":"identity-token","expires":1}`, store.KindOAuth)
	key, err := accounts.PiInferenceKey(t.Context(), store.ProviderMetaMuse, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if key != "minted-key" {
		t.Fatalf("key = %q, want the minted key of the OAuth sign-in", key)
	}
}

// A pasted Muse key still reads as the key.
func TestPiInferenceKeyReadsAPastedKey(t *testing.T) {
	accounts := testAccounts(t)
	seedPiLogin(t, accounts, "meta", `{"type":"api_key","key":"pasted-key"}`, store.KindAPIKey)
	key, err := accounts.PiInferenceKey(t.Context(), store.ProviderMetaMuse, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if key != "pasted-key" {
		t.Fatalf("key = %q, want the pasted key", key)
	}
}

// A provider that signs in without an account ID, such as xAI, still gives the
// search tool its access token, and the same repository scope applies to it.
func TestPiAccessTokenReadsASignInWithoutAnAccountID(t *testing.T) {
	accounts := testAccounts(t)
	limited := seedPiLogin(t, accounts, "xai", `{"type":"oauth","access":"xai-token","refresh":"refresh-token","expires":1}`, store.KindOAuth)
	if err := accounts.SetRepos(limited.ID, []string{"github.com/acme/work"}); err != nil {
		t.Fatal(err)
	}
	token, err := accounts.PiAccessToken(t.Context(), store.ProviderXAI, "", "github.com/acme/work")
	if err != nil {
		t.Fatal(err)
	}
	if token != "xai-token" {
		t.Fatalf("token = %q, want the saved xAI sign-in", token)
	}
	if token, err := accounts.PiAccessToken(t.Context(), store.ProviderXAI, "", "github.com/acme/personal"); err == nil {
		t.Fatalf("another repository read %q, want the scope refused", token)
	}
	if _, err := accounts.PiAccessToken(t.Context(), store.ProviderKimiCode, "", ""); !errors.Is(err, native.ErrNotSignedIn) {
		t.Fatalf("another provider: err = %v, want ErrNotSignedIn", err)
	}
}

func TestPiProviderKeyReadsTheSavedKey(t *testing.T) {
	r := providerKeysRuntime(t)
	if _, err := r.AddProviderKey(t.Context(), "pi", store.ProviderBrave, "", "brave-key"); err != nil {
		t.Fatal(err)
	}
	key, err := r.Accounts.PiProviderKey(t.Context(), store.ProviderBrave, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if key != "brave-key" {
		t.Fatalf("key = %q, want the saved Brave key", key)
	}
	if _, err := r.Accounts.PiProviderKey(t.Context(), store.ProviderZAI, "", ""); !errors.Is(err, native.ErrNotSignedIn) {
		t.Fatalf("another provider: err = %v, want ErrNotSignedIn", err)
	}
}
