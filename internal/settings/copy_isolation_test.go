package settings

import (
	"path/filepath"
	"testing"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/store"
	"dyna-agent/internal/store/storetest"
)

func TestCopyUsesItsOwnSettings(t *testing.T) {
	dir := storetest.Isolate(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	db, err := database.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := DefaultPath(); got != filepath.Join(store.ConfigDir(), "settings.json") {
		t.Fatalf("unmarked settings path = %q", got)
	}
	if _, err := db.Exec("CREATE TABLE dyna_copy_marker (id INTEGER PRIMARY KEY, copied_at TEXT NOT NULL); INSERT INTO dyna_copy_marker VALUES (1, ?)", time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if got := DefaultPath(); got != filepath.Join(dir, "settings.json") {
		t.Fatalf("copied settings path = %q", got)
	}
}
