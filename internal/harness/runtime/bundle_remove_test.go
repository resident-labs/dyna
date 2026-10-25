package runtime

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	accountstore "dyna-agent/internal/account/store"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/store"
)

func TestPrivateBundleRemovalProtectsPinsAndAccounts(t *testing.T) {
	accounts := testAccounts(t)
	account, err := accounts.CreateProvider("pi", accountstore.ProviderChatGPT, accountstore.KindOAuth, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(account.Home, "owner-sentinel")
	if err = os.WriteFile(sentinel, []byte("saved login fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	manager := manager(t)
	old := install(t, manager, "pi", "1.1.0")
	_ = install(t, manager, "pi", "2.0.0")
	_ = install(t, manager, "pi", "3.0.0")
	ctx := context.Background()
	if err = manager.Remove(ctx, "pi", old.Version, nil, false); err == nil {
		t.Fatal("bundle removed without confirmation")
	}
	lease, err := manager.LeaseManaged(ctx, old)
	if err != nil {
		t.Fatal(err)
	}
	if err = manager.Remove(ctx, "pi", old.Version, nil, true); err == nil {
		t.Fatal("live bundle removed")
	}
	if err = lease.Close(); err != nil {
		t.Fatal(err)
	}
	if err = manager.Remove(ctx, "pi", old.Version, []Bundle{old}, true); err == nil {
		t.Fatal("pinned bundle removed")
	}
	if err = manager.Remove(ctx, "pi", "../../accounts", nil, true); err == nil {
		t.Fatal("unsafe version accepted")
	}
	if err = manager.Remove(ctx, "pi", old.Version, nil, true); err != nil {
		t.Fatal(err)
	}
	if _, err = manager.Resolve("pi", old.Version); err == nil {
		t.Fatal("unused bundle remains")
	}
	raw, err := os.ReadFile(sentinel)
	if err != nil || string(raw) != "saved login fixture" {
		t.Fatal("account data changed", err)
	}
}
func TestSelectedProfileVersionsProtectBundlesWithoutRewritingProfiles(t *testing.T) {
	testAccounts(t)
	manager := manager(t)
	old := install(t, manager, "pi", "1.1.0")
	_ = install(t, manager, "pi", "2.0.0")
	_ = install(t, manager, "pi", "3.0.0")
	path := profile.DefaultPath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteJSON(path, map[string]any{"profiles": []map[string]string{{"name": "selected", "runtime": "dyna", "harness": "pi", "runtimeVersion": "1.1.0"}}}, 0600); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	pins, err := OpenPins()
	if err != nil || len(pins) != 1 {
		t.Fatal(pins, err)
	}
	if err = manager.GC(pins); err != nil {
		t.Fatal(err)
	}
	if _, err = manager.Resolve("pi", old.Version); err != nil {
		t.Fatal("selected profile lost its exact version", err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("pin discovery rewrote profiles")
	}
}

func TestUninstallRemovesEveryCopySoNoOlderOneTakesOver(t *testing.T) {
	testAccounts(t)
	manager := manager(t)
	old := install(t, manager, "pi", "1.1.0")
	selected := install(t, manager, "pi", "2.0.0")
	other := install(t, manager, "claude", "1.0.0")
	if err := manager.Select("pi", selected.Version); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := manager.Uninstall(ctx, "pi", nil, false); err == nil {
		t.Fatal("uninstalled without confirmation")
	}
	if err := manager.Uninstall(ctx, "pi", []Bundle{old}, true); err == nil {
		t.Fatal("uninstalled a pinned copy")
	}
	if err := manager.Uninstall(ctx, "pi", nil, true); err != nil {
		t.Fatal(err)
	}
	if v, _ := manager.SelectedVersion("pi"); v != "" {
		t.Fatalf("selection kept %q", v)
	}
	if b, err := manager.Resolve("pi", ""); err == nil {
		t.Fatalf("an older copy took over: %+v", b)
	}
	if _, err := manager.Resolve("claude", other.Version); err != nil {
		t.Fatal("another harness lost its copy", err)
	}
}
