package hooks

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/database"
)

func TestStartupRejectsSnapshotBeforeMigration(t *testing.T) {
	for _, complete := range []bool{false, true} {
		name := "fresh"
		if complete {
			name = "complete"
		}
		t.Run(name, func(t *testing.T) {
			data := t.TempDir()
			t.Cleanup(func() { database.Close(data) })
			db, err := database.Open(data)
			if err != nil {
				t.Fatal(err)
			}
			if complete {
				if err := Migrate(data); err != nil {
					t.Fatal(err)
				}
			}
			before := map[string]string{}
			rows, err := db.Query("SELECT key,value FROM meta WHERE key LIKE 'hooks.runtime%'")
			if err != nil {
				t.Fatal(err)
			}
			for rows.Next() {
				var key, value string
				if err := rows.Scan(&key, &value); err != nil {
					t.Fatal(err)
				}
				before[key] = value
			}
			if err := rows.Close(); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(data, "hook-spool", "1.json")
			source := []byte("{\"event\":\"before-land\",\"snapshot\":true}")
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, source, 0600); err != nil {
				t.Fatal(err)
			}
			if err := Migrate(data); err == nil || !strings.Contains(err.Error(), path) {
				t.Errorf("startup must reject source path %s: %v", path, err)
			}
			rows, err = db.Query("SELECT key,value FROM meta WHERE key LIKE 'hooks.runtime%'")
			if err != nil {
				t.Fatal(err)
			}
			after := map[string]string{}
			for rows.Next() {
				var key, value string
				if err := rows.Scan(&key, &value); err != nil {
					t.Fatal(err)
				}
				after[key] = value
			}
			if err := rows.Close(); err != nil {
				t.Fatal(err)
			}
			if len(after) != len(before) {
				t.Errorf("migration changed keys: before %v, after %v", before, after)
			}
			for key, value := range before {
				if after[key] != value {
					t.Errorf("migration changed %s", key)
				}
			}
			if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, source) {
				t.Fatalf("source changed: %q: %v", got, err)
			}
		})
	}
}
