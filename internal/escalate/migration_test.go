package escalate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"dyna-agent/internal/database"
	"dyna-agent/internal/state"
	"dyna-agent/internal/store/storetest"
)

// This fixture comes from a retained escalation JSON record. The key, session,
// and profile have been replaced; the field names and timestamp are unchanged.
func oldDedupe(t *testing.T, dir string) (record, []byte, string) {
	t.Helper()
	raw, err := os.ReadFile("testdata/dedupe-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var rec record
	if err := json.Unmarshal(raw, &rec); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(rec.Key))
	path := filepath.Join(dir, "escalations", hex.EncodeToString(sum[:])[:16]+".json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return rec, raw, path
}

func TestMigrationImportsDedupeBeforeRouting(t *testing.T) {
	for _, previousSQLite := range []bool{false, true} {
		name := "json-only"
		if previousSQLite {
			name = "sqlite-v1"
		}
		t.Run(name, func(t *testing.T) {
			dir := storetest.Isolate(t)
			t.Cleanup(func() { database.Close(dir) })
			rec, raw, path := oldDedupe(t, dir)
			db, err := database.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			if previousSQLite {
				err = database.Migrate(db, "escalate.version", 1, func(conn *sql.Conn, _ int) error {
					_, err := conn.ExecContext(context.Background(), `CREATE TABLE escalation_records (
key TEXT PRIMARY KEY, session TEXT NOT NULL, profile TEXT NOT NULL, at TEXT NOT NULL)`)
					return err
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := Migrate(dir); err != nil {
				t.Fatal(err)
			}
			got, found, err := readRecord(context.Background(), state.For(db), rec.Key)
			if err != nil || !found || got.Key != rec.Key || got.Session != rec.Session || got.Profile != rec.Profile || !got.At.Equal(rec.At) {
				t.Fatalf("startup record = %+v, %v, %v", got, found, err)
			}
			kept, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(kept, raw) {
				t.Fatalf("old source changed: %v", err)
			}
			// SQLite stays authoritative after migration, even if the old file changes.
			if _, err := db.Exec("UPDATE escalation_records SET profile=? WHERE key=?", "current-profile", rec.Key); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := database.Close(dir); err != nil {
				t.Fatal(err)
			}
			if err := Migrate(dir); err != nil {
				t.Fatal(err)
			}
			db, err = database.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			got, found, err = readRecord(context.Background(), state.For(db), rec.Key)
			if err != nil || !found || got.Profile != "current-profile" {
				t.Fatalf("replay record = %+v, %v, %v", got, found, err)
			}
		})
	}
}

func TestMigrationRejectsDamagedDedupeAtomically(t *testing.T) {
	for _, raw := range []string{"{", "null", "{}", `{"key":"other","session":"s","at":"2026-10-20T00:00:00Z"}`} {
		t.Run(raw, func(t *testing.T) {
			dir := storetest.Isolate(t)
			t.Cleanup(func() { database.Close(dir) })
			rec, original, path := oldDedupe(t, dir)
			if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			if err := Migrate(dir); err == nil {
				t.Fatal("damaged dedupe accepted")
			}
			db, err := database.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			var version string
			if err := db.QueryRow("SELECT value FROM meta WHERE key=?", "escalate.version").Scan(&version); err != sql.ErrNoRows {
				t.Fatalf("failed migration committed version %q: %v", version, err)
			}
			if err := os.WriteFile(path, original, 0600); err != nil {
				t.Fatal(err)
			}
			if err := Migrate(dir); err != nil {
				t.Fatal(err)
			}
			got, found, err := readRecord(context.Background(), state.For(db), rec.Key)
			if err != nil || !found || got.Session != rec.Session {
				t.Fatalf("repaired import = %+v, %v, %v", got, found, err)
			}
		})
	}
}

func TestMigrationKeepsCanonicalDedupeBeforeReadingStaleSource(t *testing.T) {
	for _, source := range []string{"valid", "malformed", "nonregular"} {
		t.Run(source, func(t *testing.T) {
			dir := storetest.Isolate(t)
			t.Cleanup(func() { database.Close(dir) })
			rec, raw, path := oldDedupe(t, dir)
			if source == "malformed" {
				raw = []byte("{")
				if err := os.WriteFile(path, raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if source == "nonregular" {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			db, err := database.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			if err := database.Migrate(db, "escalate.version", 1, func(conn *sql.Conn, _ int) error {
				if _, err := conn.ExecContext(t.Context(), `CREATE TABLE escalation_records (key TEXT PRIMARY KEY,session TEXT NOT NULL,profile TEXT NOT NULL,at TEXT NOT NULL)`); err != nil {
					return err
				}
				rec.Profile = "canonical-profile"
				return putRecord(t.Context(), conn, rec)
			}); err != nil {
				t.Fatal(err)
			}
			for replay := 0; replay < 2; replay++ {
				if err := Migrate(dir); err != nil {
					t.Fatalf("replay %d read stale source: %v", replay, err)
				}
				got, found, err := readRecord(t.Context(), state.For(db), rec.Key)
				if err != nil || !found || got.Profile != "canonical-profile" || !got.At.Equal(rec.At) {
					t.Fatalf("canonical record changed: %+v, %v, %v", got, found, err)
				}
			}
			if source == "nonregular" {
				info, err := os.Lstat(path)
				if err != nil || !info.IsDir() {
					t.Fatalf("retained directory changed: %v", err)
				}
			} else {
				got, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(got, raw) {
					t.Fatal("retained source changed")
				}
			}
		})
	}
}

func TestMigrationRefusesNonregularRequiredDedupe(t *testing.T) {
	dir := storetest.Isolate(t)
	t.Cleanup(func() { database.Close(dir) })
	_, _, path := oldDedupe(t, dir)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(dir); err == nil {
		t.Fatal("nonregular required source accepted")
	}
	db, err := database.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	var v string
	if err := db.QueryRow(`SELECT value FROM meta WHERE key='escalate.version'`).Scan(&v); err != sql.ErrNoRows {
		t.Fatalf("failed migration committed version %q: %v", v, err)
	}
}
