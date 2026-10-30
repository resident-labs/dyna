package state

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
)

// mirrorFixture is a store with one record, "value", and its copy at path.
type mirrorFixture struct {
	t    *testing.T
	w    *Writer
	path string
}

func newMirrorFixture(t *testing.T) *mirrorFixture {
	w, dir := mirrorWriter(t)
	if err := MigrateMirrors(dir); err != nil {
		t.Fatal(err)
	}
	return &mirrorFixture{t: t, w: w, path: filepath.Join(t.TempDir(), "record.json")}
}

// mirrorWriter is a Writer for a fresh database with a things table, and the
// database's folder.
func mirrorWriter(t *testing.T) (*Writer, string) {
	t.Helper()
	dir := t.TempDir()
	db, err := database.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close(dir) })
	if _, err := db.Exec("CREATE TABLE things(name TEXT)"); err != nil {
		t.Fatal(err)
	}
	return newWriter(db), dir
}

// commit saves value as the record, as a store's write does.
func (f *mirrorFixture) commit(value string) {
	f.t.Helper()
	if err := f.w.Write(context.Background(), hub.Event{}, func(conn *sql.Conn) error {
		if _, err := conn.ExecContext(context.Background(), "DELETE FROM things"); err != nil {
			return err
		}
		_, err := conn.ExecContext(context.Background(), "INSERT INTO things(name) VALUES (?)", value)
		return err
	}); err != nil {
		f.t.Fatal(err)
	}
}

// record is the committed value.
func (f *mirrorFixture) record() string {
	f.t.Helper()
	var value string
	if err := f.w.db.QueryRow("SELECT name FROM things").Scan(&value); err != nil {
		f.t.Fatal(err)
	}
	return value
}

// oldBinaryWrites replaces the copy the way the previous release does: a new
// file renamed onto path.
func (f *mirrorFixture) oldBinaryWrites(value string) {
	f.t.Helper()
	tmp := f.path + ".old"
	if err := os.WriteFile(tmp, []byte(value), 0o600); err != nil {
		f.t.Fatal(err)
	}
	if err := os.Rename(tmp, f.path); err != nil {
		f.t.Fatal(err)
	}
}

func (f *mirrorFixture) file() string {
	f.t.Helper()
	raw, err := os.ReadFile(f.path)
	if err != nil {
		f.t.Fatal(err)
	}
	return string(raw)
}

// ForgetMirrors removes only the requested key and its children.
func TestForgetMirrorsDropsAKeyAndItsChildren(t *testing.T) {
	f := newMirrorFixture(t)
	for _, key := range []string{"test/record", "test/record/child", "test/recorder"} {
		if _, err := f.w.db.Exec("INSERT INTO state_mirrors(key, version) VALUES (?, ?)", key, "v1"); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.w.Write(context.Background(), hub.Event{}, func(conn *sql.Conn) error { return ForgetMirrors(context.Background(), conn, "test/record") }); err != nil {
		t.Fatal(err)
	}
	rows, err := f.w.db.Query("SELECT key FROM state_mirrors")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatal("sibling stamp missing")
	}
	var key string
	if err := rows.Scan(&key); err != nil {
		t.Fatal(err)
	}
	if key != "test/recorder" || rows.Next() {
		t.Fatalf("unexpected remaining stamp %q", key)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}
