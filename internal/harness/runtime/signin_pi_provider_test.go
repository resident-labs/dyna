package runtime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func piAuthTypes(t *testing.T, home string) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(home, "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	var file map[string]struct{ Type string }
	if json.Unmarshal(raw, &file) != nil {
		t.Fatalf("auth.json: %s", raw)
	}
	types := map[string]string{}
	for id, entry := range file {
		types[id] = entry.Type
	}
	return types
}

// A provider sign-in adds the provider to Pi's one login. It makes no account.
func TestPiProviderSignInJoinsTheSingleLogin(t *testing.T) {
	r := providerKeysRuntime(t)
	bundle := install(t, r.Bundles, "pi", "1.3.0")
	ctx := context.Background()
	if _, err := r.HarnessProviderKeys(ctx, "pi", "add", "zai", testProviderKey); err != nil {
		t.Fatal(err)
	}
	flow, err := r.piProviderFlow(bundle.Executable)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(flow.Cwd, "auth.json"), []byte(`{"kimi-coding":{"type":"oauth","access":"fake","refresh":"fake","expires":1}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = flow.Complete(ctx); err != nil {
		t.Fatal(err)
	}
	accounts, _ := r.Accounts.List()
	active, _ := r.Accounts.Active("pi")
	home, _ := r.Accounts.HarnessHome("pi")
	for name, dir := range map[string]string{"runtime": home, "account": active.Home} {
		types := piAuthTypes(t, dir)
		if types["kimi-coding"] != "oauth" || types["zai"] != "api_key" {
			t.Fatalf("%s login = %v", name, types)
		}
	}
	if len(accounts) != 1 {
		t.Fatalf("sign-in made %d accounts", len(accounts))
	}
	if _, err = os.Stat(flow.Cwd); !os.IsNotExist(err) {
		t.Fatalf("scratch home stayed: %v", err)
	}
	got, err := r.HarnessProviderKeys(ctx, "pi", "remove", "kimi-coding", "")
	if err != nil || len(got.Providers) != 1 || got.Providers[0].ID != "zai" {
		t.Fatalf("remove a sign-in: %+v %v", got, err)
	}
}

// The old design saved each provider sign-in as an account of its own. They merge once.
func TestMigratePiAccountsMovesSignInsIntoTheLogin(t *testing.T) {
	r := providerKeysRuntime(t)
	install(t, r.Bundles, "pi", "1.3.0")
	ctx := context.Background()
	if _, err := r.HarnessProviderKeys(ctx, "pi", "add", "zai", testProviderKey); err != nil {
		t.Fatal(err)
	}
	extra, err := r.Accounts.Create("pi", "kimi")
	if err != nil {
		t.Fatal(err)
	}
	// The extra account also repeats zai with another key: the active login wins.
	body := `{"kimi-coding":{"type":"oauth","access":"fake"},"zai":{"type":"api_key","key":"other"}}`
	if err = os.WriteFile(filepath.Join(extra.Home, "auth.json"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	r.MigratePiAccounts(ctx)
	if accounts, _ := r.Accounts.List(); len(accounts) != 1 || accounts[0].ID == extra.ID {
		t.Fatalf("accounts after migration: %+v", accounts)
	}
	home, _ := r.Accounts.HarnessHome("pi")
	raw, _ := os.ReadFile(filepath.Join(home, "auth.json"))
	types := piAuthTypes(t, home)
	if types["kimi-coding"] != "oauth" || strings.Contains(string(raw), "other") {
		t.Fatalf("runtime login after migration: %s", raw)
	}
	active, _ := r.Accounts.Active("pi")
	if piAuthTypes(t, active.Home)["kimi-coding"] != "oauth" {
		t.Fatal("the active account lost the migrated sign-in")
	}
}
