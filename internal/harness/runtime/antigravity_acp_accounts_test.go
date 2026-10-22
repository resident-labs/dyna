package runtime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

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
