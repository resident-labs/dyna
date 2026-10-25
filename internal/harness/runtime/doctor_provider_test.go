package runtime

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	accountstore "dyna-agent/internal/account/store"
	harnessmgr "dyna-agent/internal/harness/config"
	"dyna-agent/internal/profile"
)

func TestDoctorResolvesThePiProviderFromTheProfileModel(t *testing.T) {
	accounts := testAccounts(t)
	account, err := accounts.CreateProvider("pi", accountstore.ProviderZAI, accountstore.KindAPIKey, "zai")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(account.Home, "auth.json"), []byte(`{"zai":{"type":"api_key","key":"fixture-key"}}`), 0600); err != nil {
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
	install(t, bundles, "pi", "1.4.0")
	r := &Runtime{Bundles: bundles, Accounts: accounts, Config: harnessmgr.NewConfigManager(), Generations: filepath.Join(t.TempDir(), "generations")}
	d := r.Doctor(context.Background(), profile.Profile{Name: "pi-zai", Harness: "pi", Model: "zai/glm-5.3", RuntimeVersion: "1.4.0"})
	if d.AccountID != captured.ID {
		t.Fatalf("Doctor account = %q, want %q (%+v)", d.AccountID, captured.ID, d)
	}
	if d.Status == "not signed in" {
		t.Fatalf("Doctor did not find the Pi account: %+v", d)
	}
}
