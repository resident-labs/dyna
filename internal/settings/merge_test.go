package settings

import (
	"os"
	"path/filepath"
	"testing"
)

// A settings.json saved with the removed merge steward still loads, and a save
// leaves the stale key alone.
// TODO: legacy/shim — remove with the legacy merge reader in merge.go.
func TestLegacyMergeStewardProfileIsIgnored(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"merge":{"stewardProfile":"sol"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	v, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if err := v.Set(KeySessionCheckInInterval, "5m"); err != nil {
		t.Fatal(err)
	}
	if err := v.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}
	saved, err := Load(path)
	if err != nil || saved.SessionCheckInInterval.String() != "5m0s" {
		t.Fatalf("saved = %+v, %v", saved, err)
	}
}
