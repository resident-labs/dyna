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
	harnesshome "dyna-agent/internal/harness/home"
)

// agySignInFixture stands in for the ACP sign-in: it writes the grant and the
// identity Google confirmed into the login flow's HOME.
func agySignInFixture(t *testing.T, flow *NativeLogin, sub string) {
	t.Helper()
	home := harnesshome.Home(flow.Cwd)
	files := map[string]any{
		native.AntigravityACPTokenFile: map[string]string{"client_id": "fake-client-" + sub, "client_secret": "fake-secret-" + sub, "refresh_token": "fake-refresh-" + sub},
		native.AntigravityAccountFile:  map[string]string{"identity": "google:" + sub, "label": sub + "@example.test"},
	}
	for name, value := range files {
		raw, _ := json.Marshal(value)
		path := filepath.Join(home, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

// An ACP grant alone signs in a new Antigravity account, also while another
// account is active, and switching between them needs no CLI token.
func TestAntigravityACPGrantAloneSavesAndActivatesAccounts(t *testing.T) {
	a := testAccounts(t)
	signIn := func(sub string) NativeAccount {
		t.Helper()
		n, err := a.Create("antigravity", "")
		if err != nil {
			t.Fatal(err)
		}
		flow, err := a.BeginLogin(context.Background(), n.ID, "/fake/agy")
		if err != nil {
			t.Fatal(err)
		}
		agySignInFixture(t, flow, sub)
		got, err := flow.Complete(context.Background())
		if err != nil {
			t.Fatalf("an ACP grant alone was not saved: %v", err)
		}
		if !got.Active || got.Identity != "google:"+sub || got.Label != sub+"@example.test" {
			t.Fatalf("saved %+v", got)
		}
		return got
	}
	first := signIn("first")
	signIn("second")
	if n, err := a.Find(first.ID); err != nil || n.Identity != "google:first" || n.Active {
		t.Fatalf("the outgoing account changed: %+v %v", n, err)
	}
	if err := a.Switch(context.Background(), first.ID, "/fake/agy"); err != nil {
		t.Fatalf("switching back failed: %v", err)
	}
	scope, _ := a.HarnessHome("antigravity")
	raw, err := os.ReadFile(filepath.Join(harnesshome.Home(scope), native.AntigravityAccountFile))
	if err != nil {
		t.Fatal(err)
	}
	var record struct{ Identity string }
	_ = json.Unmarshal(raw, &record)
	if record.Identity != "google:first" {
		t.Fatalf("the HOME holds %q after switching to the first account", record.Identity)
	}
	if _, err := os.Stat(filepath.Join(harnesshome.Home(scope), ".gemini")); !os.IsNotExist(err) {
		t.Fatal("an agy CLI token was written")
	}
}

// A HOME signed in before ACP-only accounts has the active account's grant but
// no recorded identity. The upgrade records it from the active account, so
// that account stays signed in and selectable.
func TestAntigravityUpgradeRecordsTheActiveAccount(t *testing.T) {
	a := testAccounts(t)
	n, err := a.Create("antigravity", "")
	if err != nil {
		t.Fatal(err)
	}
	flow, err := a.BeginLogin(context.Background(), n.ID, "/fake/agy")
	if err != nil {
		t.Fatal(err)
	}
	agySignInFixture(t, flow, "legacy")
	if _, err = flow.Complete(context.Background()); err != nil {
		t.Fatal(err)
	}
	scope, _ := a.HarnessHome("antigravity")
	record := filepath.Join(harnesshome.Home(scope), native.AntigravityAccountFile)
	if err = os.Remove(record); err != nil {
		t.Fatal(err)
	}
	if err = a.MigrateLegacyGrants(); err != nil {
		t.Fatal(err)
	}
	if err = a.Switch(context.Background(), n.ID, "/fake/agy"); err != nil {
		t.Fatalf("the active account was lost in the upgrade: %v", err)
	}
	raw, err := os.ReadFile(record)
	var got struct{ Identity, Label string }
	if err != nil || json.Unmarshal(raw, &got) != nil || got.Identity != "google:legacy" || got.Label != "legacy@example.test" {
		t.Fatalf("the upgrade recorded %s: %v", raw, err)
	}
}

// ACP-only accounts have nothing to refresh, so a refresh never takes the
// switch gate that new turns wait on.
func TestAntigravityACPAccountSkipsCredentialRefresh(t *testing.T) {
	a := testAccounts(t)
	signIn := func(sub string) (NativeAccount, *NativeLogin) {
		t.Helper()
		n, err := a.Create("antigravity", "")
		if err != nil {
			t.Fatal(err)
		}
		flow, err := a.BeginLogin(context.Background(), n.ID, "/fake/agy")
		if err != nil {
			t.Fatal(err)
		}
		agySignInFixture(t, flow, sub)
		return n, flow
	}
	first, flow := signIn("first")
	if _, err := flow.Complete(context.Background()); err != nil {
		t.Fatal(err)
	}
	// An open sign-in holds the switch gate; a refresh that took it would block.
	_, open := signIn("second")
	defer func() { _ = open.Cancel() }()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	result, err := a.RefreshCredentials(ctx, first.ID, "/fake/agy")
	if result.Supported || ctx.Err() != nil || !errors.Is(err, native.ErrUnsupported) {
		t.Fatalf("an ACP-only account was refreshed: %+v %v", result, err)
	}
}
