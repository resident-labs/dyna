package shells

import (
	"path/filepath"
	"testing"
	"time"

	"dyna-agent/internal/database"
)

func TestShellWaitsForEngineSchemaUpgrade(t *testing.T) {
	data := t.TempDir()
	s := &Store{Root: filepath.Join(data, "shells")}
	meta, err := s.Create(StartOptions{Cmd: []string{"true"}, Cwd: data})
	if err != nil {
		t.Fatal(err)
	}
	db, err := s.db()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP INDEX shell_jobs_prune; ALTER TABLE shell_jobs DROP COLUMN activity_output; ALTER TABLE shell_jobs DROP COLUMN activity_lease; ALTER TABLE shell_jobs DROP COLUMN activity_copier; ALTER TABLE shell_jobs DROP COLUMN activity_cpu; UPDATE meta SET value='5' WHERE key='shells.version'`); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(data); err != nil {
		t.Fatal(err)
	}
	database.DisownSchema()
	t.Cleanup(database.OwnSchema)
	waits := 0
	s.pause = func(time.Duration) { waits++ }
	if _, err := s.ReadMeta(meta.ID); err == nil || waits != 0 {
		t.Fatalf("failed engine migration: waits = %d, error = %v", waits, err)
	}
	// Simulate an external process, whose engine did not report a migration failure.
	s.schemaRetry = func(err error) bool { return database.IsOlderSchema(err, "shells.version") }
	s.pause = func(d time.Duration) {
		waits++
		if d != time.Second {
			t.Fatalf("retry interval = %v", d)
		}
		database.OwnSchema()
		if err := Migrate(data); err != nil {
			t.Fatal(err)
		}
		database.DisownSchema()
	}
	got, err := s.ReadMeta(meta.ID)
	if err != nil || got.ID != meta.ID || waits != 1 {
		t.Fatalf("job = %q, waits = %d, error = %v", got.ID, waits, err)
	}
	// If the engine never upgrades, the caller gets the original error after
	// the bounded wait. A newer database is not an upgrade-in-progress error.
	for _, tc := range []struct {
		version string
		waits   int
	}{{"5", 120}, {"99", 0}} {
		database.OwnSchema()
		db, err = database.Open(data)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(`UPDATE meta SET value=? WHERE key='shells.version'`, tc.version); err != nil {
			t.Fatal(err)
		}
		if err = database.Close(data); err != nil {
			t.Fatal(err)
		}
		database.DisownSchema()
		waits = 0
		s.pause = func(time.Duration) { waits++ }
		_, err = s.ReadMeta(meta.ID)
		if err == nil || waits != tc.waits {
			t.Fatalf("schema %s: waits = %d, error = %v", tc.version, waits, err)
		}
	}
}
