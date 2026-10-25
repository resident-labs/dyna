package runtime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"dyna-agent/internal/account/store"
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

// A provider sign-in gives the provider its own account and home, and stores
// only that provider's entry there. The shared login is left alone.
func TestPiProviderSignInGivesEachProviderItsOwnAccount(t *testing.T) {
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
	if len(accounts) != 2 {
		t.Fatalf("sign-in made %d accounts, want 2", len(accounts))
	}
	want := map[store.Provider]string{store.ProviderZAI: "api_key", store.ProviderKimiCode: "oauth"}
	entry := map[store.Provider]string{store.ProviderZAI: "zai", store.ProviderKimiCode: "kimi-coding"}
	for _, n := range accounts {
		if n.Status != "ready" || !n.Active {
			t.Fatalf("account %+v is not the active one of its provider", n)
		}
		if want[n.Provider] != string(n.Kind) {
			t.Fatalf("account provider %q kind %q", n.Provider, n.Kind)
		}
		types := piAuthTypes(t, n.Home)
		if len(types) != 1 || types[entry[n.Provider]] != want[n.Provider] {
			t.Fatalf("account home of %q holds %v", n.Provider, types)
		}
	}
	home, _ := r.Accounts.HarnessHome("pi")
	if _, err = os.Stat(filepath.Join(home, "auth.json")); !os.IsNotExist(err) {
		t.Fatalf("the shared login was written: %v", err)
	}
	if _, err = os.Stat(flow.Cwd); !os.IsNotExist(err) {
		t.Fatalf("scratch home stayed: %v", err)
	}
	got, err := r.HarnessProviderKeys(ctx, "pi", "remove", "kimi-coding", "")
	if err != nil || len(got.Providers) != 1 || got.Providers[0].ID != "zai" {
		t.Fatalf("remove a sign-in: %+v %v", got, err)
	}
}

// The split gives every provider of the shared login its own account and home,
// and writes only that provider's entry into the home. The shared login keeps
// serving turns that started before the split.
func TestSplitPiAccountsGivesEachProviderItsOwnHome(t *testing.T) {
	r := providerKeysRuntime(t)
	install(t, r.Bundles, "pi", "1.3.0")
	ctx := context.Background()
	legacy, err := r.Accounts.Create("pi", "Pi login")
	if err != nil {
		t.Fatal(err)
	}
	shared, _ := r.Accounts.HarnessHome("pi")
	if err = os.MkdirAll(shared, 0700); err != nil {
		t.Fatal(err)
	}
	body := `{"kimi-coding":{"type":"oauth","access":"fake"},"zai":{"type":"api_key","key":"saved"}}`
	if err = os.WriteFile(filepath.Join(shared, "auth.json"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	// The legacy account home holds a provider the shared login lost.
	if err = os.WriteFile(filepath.Join(legacy.Home, "auth.json"), []byte(`{"deepseek":{"type":"api_key","key":"older"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	// A credential limited to a repository must not become an unrestricted one.
	if err = r.Accounts.SetRepos(legacy.ID, []string{"github.com/acme/private"}); err != nil {
		t.Fatal(err)
	}
	r.SplitPiAccounts(ctx)
	accounts, _ := r.Accounts.List()
	if len(accounts) != 3 {
		t.Fatalf("split left %d accounts, want 3", len(accounts))
	}
	seen := map[store.Provider]bool{}
	for _, n := range accounts {
		if n.Provider == "" || n.Status != "ready" {
			t.Fatalf("account after the split: %+v", n)
		}
		seen[n.Provider] = true
		types := piAuthTypes(t, n.Home)
		if len(types) != 1 || types[piEntryID(n.Provider)] == "" {
			t.Fatalf("home of %q holds %v", n.Provider, types)
		}
		if !n.Active {
			t.Fatalf("%q is not the active account of its provider", n.Provider)
		}
		if !slices.Equal(n.Repos, []string{"github.com/acme/private"}) {
			t.Fatalf("%q runs in %v, want the rules of its source account", n.Provider, n.Repos)
		}
	}
	for _, provider := range []store.Provider{store.ProviderKimiCode, store.ProviderZAI, store.ProviderDeepSeek} {
		if !seen[provider] {
			t.Fatalf("the split has no %q account", provider)
		}
	}
	// The legacy home keeps its history and now holds one provider only.
	if !hasHome(accounts, legacy.ID) {
		t.Fatal("the legacy account was replaced")
	}
	raw, err := os.ReadFile(filepath.Join(shared, "auth.json"))
	if err != nil || string(raw) != body {
		t.Fatalf("shared login changed: %s %v", raw, err)
	}
	// A second start finds a split registry and changes nothing.
	before, _ := r.Accounts.List()
	r.SplitPiAccounts(ctx)
	after, _ := r.Accounts.List()
	if len(after) != len(before) {
		t.Fatalf("the split ran twice: %d then %d accounts", len(before), len(after))
	}

	// A start that stopped before writing one entry finishes it, into the account
	// the record names, without a second account for that provider.
	zai := after[0]
	for _, n := range after {
		if n.Provider == store.ProviderZAI {
			zai = n
		}
	}
	db := &store.Store{Root: r.Accounts.Root}
	key := store.PiSplitKey{Source: legacy.ID, Provider: "zai"}
	if err = db.MarkPiSplitDone(ctx, key, zai.ID); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(filepath.Join(zai.Home, "auth.json")); err != nil {
		t.Fatal(err)
	}
	r.SplitPiAccounts(ctx)
	resumed, _ := r.Accounts.List()
	if len(resumed) != len(after) {
		t.Fatalf("the resumed start made %d accounts, want %d", len(resumed), len(after))
	}
	if got, err := r.Accounts.Find(zai.ID); err != nil || !piAccountHolds(got, "zai") {
		t.Fatalf("the lost entry was not restored into its account: %v", err)
	}

	// An unfinished entry that lost its rules or its login record is repaired,
	// and the account is selected again when the start stopped before that.
	if err = db.MarkPiSplitDone(ctx, key, zai.ID); err != nil {
		t.Fatal(err)
	}
	if err = r.Accounts.SetRepos(zai.ID, nil); err != nil {
		t.Fatal(err)
	}
	if err = r.Accounts.transaction(func(s *store.State) error {
		s.Find(zai.ID).NeedsLogin = true
		return nil
	}, true); err != nil {
		t.Fatal(err)
	}
	if err = db.SetActive(store.ProviderZAI, "pi", "", ""); err != nil {
		t.Fatal(err)
	}
	r.SplitPiAccounts(ctx)
	repaired, _ := r.Accounts.List()
	for _, n := range repaired {
		if n.ID != zai.ID {
			continue
		}
		if n.Status != "ready" || !slices.Equal(n.Repos, []string{"github.com/acme/private"}) || !n.Active {
			t.Fatalf("the unfinished account stayed unfinished: %+v", n)
		}
	}

	// A finished entry keeps the owner's later change: restricting the account
	// must not look like unfinished work.
	if err = r.Accounts.SetRepos(zai.ID, []string{"github.com/acme/only"}); err != nil {
		t.Fatal(err)
	}
	r.SplitPiAccounts(ctx)
	restricted, _ := r.Accounts.List()
	for _, n := range restricted {
		if n.ID == zai.ID && !slices.Equal(n.Repos, []string{"github.com/acme/only"}) {
			t.Fatalf("the split reverted the owner's limits: %v", n.Repos)
		}
	}

	// An account the owner removed stays gone.
	if err = r.Accounts.Remove(zai.ID, true); err != nil {
		t.Fatal(err)
	}
	r.SplitPiAccounts(ctx)
	final, _ := r.Accounts.List()
	if len(final) != len(resumed)-1 {
		t.Fatalf("the split restored a removed account: %d accounts", len(final))
	}
}

// piEntryID names the auth.json entry a provider's account holds.
func piEntryID(provider store.Provider) string {
	if provider == store.ProviderKimiCode {
		return "kimi-coding"
	}
	return string(provider)
}

func hasHome(accounts []NativeAccount, id string) bool {
	for _, n := range accounts {
		if n.ID == id {
			return true
		}
	}
	return false
}

// Two sources with different providers keep their own credential and their own
// repository rules, and a credential only the shared login holds gets no rules
// from either of them.
func TestPiSplitKeepsEachSourcesCredentialAndRules(t *testing.T) {
	r := providerKeysRuntime(t)
	install(t, r.Bundles, "pi", "1.3.0")
	ctx := context.Background()
	shared, err := r.Accounts.HarnessHome("pi")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(shared, 0700); err != nil {
		t.Fatal(err)
	}
	body := `{"zai":{"type":"api_key","key":"work-key"},"anthropic":{"type":"api_key","key":"personal-key"},"xai":{"type":"api_key","key":"shared-only"}}`
	if err = os.WriteFile(filepath.Join(shared, "auth.json"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	source := func(label, entries, repo string) NativeAccount {
		t.Helper()
		n, err := r.Accounts.Create("pi", label)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(n.Home, "auth.json"), []byte(entries), 0600); err != nil {
			t.Fatal(err)
		}
		if err = r.Accounts.SetRepos(n.ID, []string{repo}); err != nil {
			t.Fatal(err)
		}
		return n
	}
	work := source("work", `{"zai":{"type":"api_key","key":"work-key"}}`, "github.com/acme/work")
	source("personal", `{"anthropic":{"type":"api_key","key":"personal-key"}}`, "github.com/acme/personal")
	// The shared login ran under the selected account, so a credential only it
	// holds keeps that account's limits.
	if err = (&store.Store{Root: r.Accounts.Root}).SetActive("", "pi", "", work.ID); err != nil {
		t.Fatal(err)
	}
	r.SplitPiAccounts(ctx)
	accounts, err := r.Accounts.List()
	if err != nil {
		t.Fatal(err)
	}
	type pair struct{ key, repo string }
	got := map[store.Provider]pair{}
	for _, n := range accounts {
		if n.Harness != "pi" || n.Provider == "" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(n.Home, "auth.json"))
		if err != nil {
			t.Fatal(err)
		}
		var entries map[string]struct{ Key string }
		if json.Unmarshal(raw, &entries) != nil {
			t.Fatalf("account login: %s", raw)
		}
		got[n.Provider] = pair{entries[piEntryID(n.Provider)].Key, strings.Join(n.Repos, ",")}
	}
	want := map[store.Provider]pair{
		store.ProviderZAI:       {"work-key", "github.com/acme/work"},
		store.ProviderAnthropic: {"personal-key", "github.com/acme/personal"},
		// Only the shared login held this one, so it keeps the limits of the
		// account that was selected when it ran.
		store.ProviderXAI: {"shared-only", "github.com/acme/work"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("derived accounts kept %v, want %v", got, want)
	}
	// The shared credential is refused outside the limits it kept.
	rules, err := r.Accounts.rules()
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range accounts {
		if n.Provider != store.ProviderXAI {
			continue
		}
		if !rules.Allows(n.ID, "github.com/acme/work") || rules.Allows(n.ID, "github.com/acme/personal") {
			t.Fatalf("the shared credential runs in the wrong repositories: %v", rules[n.ID])
		}
	}
}
