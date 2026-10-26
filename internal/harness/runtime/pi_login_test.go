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

// seedPiLogin saves one provider login in a Pi account of its own and makes the
// account active, as a finished sign-in does.
func seedPiLogin(t *testing.T, accounts *Accounts, piID, entry string, kind store.Kind) NativeAccount {
	t.Helper()
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

func TestPiChatGPTLoginReadsTheActiveSignIn(t *testing.T) {
	accounts := testAccounts(t)
	seedPiLogin(t, accounts, "openai-codex", `{"type":"oauth","access":"access-token","refresh":"refresh-token","expires":1,"accountId":"workspace"}`, store.KindOAuth)
	access, account, err := accounts.PiChatGPTLogin(t.Context(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	if access != "access-token" || account != "workspace" {
		t.Fatalf("login = %q/%q, want the saved ChatGPT sign-in", access, account)
	}
}

func TestPiChatGPTLoginWithoutASignInIsNotSignedIn(t *testing.T) {
	accounts := testAccounts(t)
	if _, _, err := accounts.PiChatGPTLogin(t.Context(), "", ""); !errors.Is(err, native.ErrNotSignedIn) {
		t.Fatalf("err = %v, want ErrNotSignedIn", err)
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

// A search from one repository never reads the login of an account the owner
// limited to another repository, and the room's own selection wins.
func TestPiChatGPTLoginKeepsTheAccountsRepositoryScope(t *testing.T) {
	accounts := testAccounts(t)
	limited := seedPiLogin(t, accounts, "openai-codex", `{"type":"oauth","access":"work-token","refresh":"refresh-token","expires":1,"accountId":"work"}`, store.KindOAuth)
	if err := accounts.SetRepos(limited.ID, []string{"github.com/acme/work"}); err != nil {
		t.Fatal(err)
	}
	access, _, err := accounts.PiChatGPTLogin(t.Context(), "", "github.com/acme/work")
	if err != nil {
		t.Fatal(err)
	}
	if access != "work-token" {
		t.Fatalf("access = %q, want the account the repository allows", access)
	}
	// The same account must not answer for another repository.
	if access, _, err = accounts.PiChatGPTLogin(t.Context(), "", "github.com/acme/personal"); err == nil {
		t.Fatalf("another repository read %q, want the scope refused", access)
	}
	// A room may select another account of the same provider, and that account's
	// own scope decides, while the global account keeps refusing that repository.
	room := seedPiLogin(t, accounts, "openai-codex", `{"type":"oauth","access":"personal-token","refresh":"refresh-token","expires":1,"accountId":"personal"}`, store.KindOAuth)
	if err := accounts.SetRepos(room.ID, []string{"github.com/acme/personal"}); err != nil {
		t.Fatal(err)
	}
	if err := accounts.setActiveAccount(limited); err != nil {
		t.Fatal(err)
	}
	if err := accounts.SelectInScope(room, "room-1"); err != nil {
		t.Fatal(err)
	}
	access, _, err = accounts.PiChatGPTLogin(t.Context(), "room-1", "github.com/acme/personal")
	if err != nil {
		t.Fatal(err)
	}
	if access != "personal-token" {
		t.Fatalf("room selection read %q, want the account the room selected", access)
	}
	if access, _, err = accounts.PiChatGPTLogin(t.Context(), "", "github.com/acme/personal"); err == nil {
		t.Fatalf("the global account read %q, want its scope to refuse the repository", access)
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
