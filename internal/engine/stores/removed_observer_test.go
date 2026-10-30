package stores

import (
	"dyna-agent/internal/database"
	"testing"
)

func TestRemovedObserverMigrationDropsBothTablesOnce(t *testing.T) {
	dir := t.TempDir()
	db, err := database.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close(dir) })
	if _, err := db.Exec(`CREATE TABLE admission_observed (id INTEGER); CREATE TABLE admission_state (id INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if err := migrateRemovedObserver(dir); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name IN ('admission_observed','admission_state')`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("remaining tables: %d, %v", count, err)
	}
	if _, err := db.Exec(`CREATE TABLE admission_state (id INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if err := migrateRemovedObserver(dir); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='admission_state'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("migration ran twice: %d, %v", count, err)
	}
}
