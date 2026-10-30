package stores

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"dyna-agent/internal/database"
)

func TestUpgradeFloor(t *testing.T) {
	floor := []floorRequirement{{key: "import.example", release: "v1.2.3"}}
	old := upgradeFloor
	upgradeFloor = floor
	t.Cleanup(func() { upgradeFloor = old })
	t.Run("legacy", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "sessions", "s", "queue.json")
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("{}"), 0600); err != nil {
			t.Fatal(err)
		}
		err := Migrate(dir)
		var floorErr *UpgradeFloorError
		if !errors.As(err, &floorErr) || floorErr.Release != "v1.2.3" {
			t.Fatalf("error = %v", err)
		}
		if _, err := os.Stat(filepath.Join(dir, "dyna.db")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("database created: %v", err)
		}
	})
	t.Run("existing", func(t *testing.T) {
		dir := t.TempDir()
		db, err := database.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { database.Close(dir) })
		err = Migrate(dir)
		var floorErr *UpgradeFloorError
		if !errors.As(err, &floorErr) || floorErr.Release != "v1.2.3" {
			t.Fatalf("error = %v", err)
		}
		var n int
		if err := db.QueryRow("SELECT count(*) FROM sqlite_schema WHERE name='state_mirrors'").Scan(&n); err != nil || n != 0 {
			t.Fatalf("migration ran: %d, %v", n, err)
		}
		if _, err := db.Exec("INSERT INTO meta(key,value) VALUES('import.example','1')"); err != nil {
			t.Fatal(err)
		}
		if err := Migrate(dir); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("new", func(t *testing.T) {
		dir := t.TempDir()
		t.Cleanup(func() { database.Close(dir) })
		if err := Migrate(dir); err != nil {
			t.Fatal(err)
		}
		if err := Migrate(dir); err != nil {
			t.Fatal(err)
		}
	})
}
