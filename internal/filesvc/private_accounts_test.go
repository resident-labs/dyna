package filesvc

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"dyna-agent/internal/harness/runtime"
)

func TestPrivateAccountsStayOutOfFileBrowsing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	account, err := runtime.NewAccounts().Create("pi", "private")
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(account.Home, "auth.json")
	if err := os.WriteFile(file, []byte("credential-canary"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := readWorkspaceBytes(file); err == nil {
		t.Fatal("account credential was readable")
	}
	if _, err := listDirs(context.Background(), account.Home); err == nil {
		t.Fatal("account root was browsable")
	}
	if err := os.Symlink(account.Home, filepath.Join(home, "linked")); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := readWorkspaceBytes(filepath.Join(home, "linked", "auth.json")); err == nil {
		t.Fatal("account alias was readable")
	}
}
