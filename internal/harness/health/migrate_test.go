package health

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"dyna-agent/internal/database"
)

func TestMigrationImportsRetainedHealthOnce(t *testing.T) {
	dataDir, sourceDir := t.TempDir(), t.TempDir()
	t.Cleanup(func() { database.Close(dataDir) })
	db, err := database.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(db, "harness.health", 1, func(conn *sql.Conn, _ int) error {
		_, err := conn.ExecContext(context.Background(), schema)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	p := testProfile()
	r := record{Fingerprint: Fingerprint(p), Version: "v1", LastHealthyVersion: "v1", LastHealthyAt: time.Unix(5, 0).UTC()}
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO harness_health(profile,record) VALUES(?,?)", key("canonical"), raw); err != nil {
		t.Fatal(err)
	}
	stale := []byte(`{"version":"stale"}`)
	path := filepath.Join(sourceDir, key(p.Name)+".json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	canonicalPath := filepath.Join(sourceDir, key("canonical")+".json")
	if err := os.WriteFile(canonicalPath, stale, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := open(dataDir, sourceDir); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{p.Name, "canonical"} {
		var got []byte
		if err := db.QueryRow("SELECT record FROM harness_health WHERE profile = ?", key(name)).Scan(&got); err != nil || string(got) != string(raw) {
			t.Fatalf("eager row %s = %s, %v", name, got, err)
		}
	}
	for path, want := range map[string][]byte{path: raw, canonicalPath: stale} {
		if got, err := os.ReadFile(path); err != nil || string(got) != string(want) {
			t.Fatalf("source changed: %s, %v", got, err)
		}
	}
	if err := os.WriteFile(path, stale, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := open(dataDir, sourceDir); err != nil {
		t.Fatal(err)
	}
	var got []byte
	if err := db.QueryRow("SELECT record FROM harness_health WHERE profile = ?", key(p.Name)).Scan(&got); err != nil || string(got) != string(raw) {
		t.Fatalf("replayed row = %s, %v", got, err)
	}
	// Service uses dataDir for its database. A stale file there must not import on View.
	if err := os.WriteFile(filepath.Join(dataDir, key(p.Name)+".json"), stale, 0600); err != nil {
		t.Fatal(err)
	}
	if v := New(dataDir).View(p, "v1"); v.LastHealthyVersion != "v1" || v.StorageError != "" {
		t.Fatalf("view = %+v", v)
	}
}
