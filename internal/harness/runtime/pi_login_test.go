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
	access, account, err := accounts.PiChatGPTLogin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if access != "access-token" || account != "workspace" {
		t.Fatalf("login = %q/%q, want the saved ChatGPT sign-in", access, account)
	}
}

func TestPiChatGPTLoginWithoutASignInIsNotSignedIn(t *testing.T) {
	accounts := testAccounts(t)
	if _, _, err := accounts.PiChatGPTLogin(t.Context()); !errors.Is(err, native.ErrNotSignedIn) {
		t.Fatalf("err = %v, want ErrNotSignedIn", err)
	}
}

func TestPiProviderKeyReadsTheSavedKey(t *testing.T) {
	r := providerKeysRuntime(t)
	if _, err := r.AddProviderKey(t.Context(), "pi", store.ProviderBrave, "", "brave-key"); err != nil {
		t.Fatal(err)
	}
	key, err := r.Accounts.PiProviderKey(t.Context(), store.ProviderBrave)
	if err != nil {
		t.Fatal(err)
	}
	if key != "brave-key" {
		t.Fatalf("key = %q, want the saved Brave key", key)
	}
	if _, err := r.Accounts.PiProviderKey(t.Context(), store.ProviderZAI); !errors.Is(err, native.ErrNotSignedIn) {
		t.Fatalf("another provider: err = %v, want ErrNotSignedIn", err)
	}
}
