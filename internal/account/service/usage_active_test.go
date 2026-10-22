package service

import (
	"os"
	"path/filepath"
	"testing"

	accountstore "dyna-agent/internal/account/store"
	"dyna-agent/internal/harness/runtime"
)

func TestAccountUsageCachedMarksActiveAccount(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store := &accountstore.Store{Root: filepath.Join(t.TempDir(), "accounts")}
	n, err := (&runtime.Accounts{Root: store.Root}).Create("codex", "Work")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.Root, "codex", ".active.json"), []byte(`{"id":"`+n.ID+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	rows := New(Options{Store: store}).AccountUsageCached().Accounts
	if len(rows) != 1 || rows[0].ID != n.ID || !rows[0].Active {
		t.Fatalf("AccountUsageCached() = %+v, want the active private account", rows)
	}
}
