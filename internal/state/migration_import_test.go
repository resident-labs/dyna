package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"dyna-agent/internal/database"
	"dyna-agent/internal/store"
)

func TestImportMigrationPreservesOwnedCopies(t *testing.T) {
	for _, mode := range []string{"version", "pending", "foreign"} {
		t.Run(mode, func(t *testing.T) {
			f := newMirrorFixture(t)
			f.commit("current")
			f.oldBinaryWrites("retained")
			info, err := os.Stat(f.path)
			if err != nil {
				t.Fatal(err)
			}
			version, pending := "", ""
			if mode == "version" {
				version = store.VersionOf(info).Key()
			} else if mode == "pending" {
				pending = fileID(info)
			}
			if _, err := f.w.db.Exec("INSERT INTO state_mirrors(key,version,pending) VALUES(?,?,?)", "test/record", version, pending); err != nil {
				t.Fatal(err)
			}
			calls := 0
			apply := func(conn *sql.Conn, _ int) error {
				imported, err := ImportMigration(t.Context(), conn, "test/record", f.path, func(conn *sql.Conn, raw []byte) error {
					calls++
					_, err := conn.ExecContext(t.Context(), "UPDATE things SET name=?", string(raw))
					return err
				})
				if err == nil && imported != (mode == "foreign") {
					return fmt.Errorf("imported = %v for %s", imported, mode)
				}
				return err
			}
			if err := database.Migrate(f.w.db, "test.import", 1, apply); err != nil {
				t.Fatal(err)
			}
			if err := database.Migrate(f.w.db, "test.import", 1, apply); err != nil {
				t.Fatal(err)
			}
			want, wantCalls := "current", 0
			if mode == "foreign" {
				want, wantCalls = "retained", 1
			}
			if got := f.record(); got != want || calls != wantCalls {
				t.Fatalf("record = %q, callback calls = %d; want %q, %d", got, calls, want, wantCalls)
			}
			if got := f.file(); got != "retained" {
				t.Fatalf("source changed to %q", got)
			}
		})
	}
}

func TestImportMigrationFreshStoreAndRollback(t *testing.T) {
	w, _ := mirrorWriter(t)
	path := filepath.Join(t.TempDir(), "record.json")
	if err := os.WriteFile(path, []byte("retained"), 0600); err != nil {
		t.Fatal(err)
	}
	stop := errors.New("abort migration")
	err := database.Migrate(w.db, "test.import", 1, func(conn *sql.Conn, _ int) error {
		imported, err := ImportMigration(t.Context(), conn, "test/record", path, func(conn *sql.Conn, raw []byte) error {
			_, err := conn.ExecContext(t.Context(), "INSERT INTO things(name) VALUES(?)", string(raw))
			return err
		})
		if err != nil {
			return err
		}
		if !imported {
			return errors.New("fresh database did not import")
		}
		return stop
	})
	if !errors.Is(err, stop) {
		t.Fatalf("migration = %v; want abort", err)
	}
	var count int
	if err := w.db.QueryRow("SELECT COUNT(*) FROM things").Scan(&count); err != nil || count != 0 {
		t.Fatalf("rolled-back records = %d, %v", count, err)
	}
	if err := w.db.QueryRow("SELECT COUNT(*) FROM meta WHERE key = 'test.import'").Scan(&count); err != nil || count != 0 {
		t.Fatalf("rolled-back version rows = %d, %v", count, err)
	}
	if err := database.Immediate(context.Background(), w.db, func(conn *sql.Conn) error {
		hasStamps, err := database.HasTable(t.Context(), conn, "state_mirrors")
		if err == nil && hasStamps {
			return errors.New("import created a rollback stamp table")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
