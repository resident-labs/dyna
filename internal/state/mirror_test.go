package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

// mirror writes the committed record as its copy.
func (f *mirrorFixture) mirror() {
	f.t.Helper()
	if err := f.w.Mirror(context.Background(), "test/record", f.path, func(out io.Writer) error {
		_, err := io.WriteString(out, f.record())
		return err
	}); err != nil {
		f.t.Fatal(err)
	}
}

// importFile imports the copy when another binary wrote it, as the record.
func (f *mirrorFixture) importFile() bool {
	f.t.Helper()
	imported, err := f.w.ImportIfForeign(context.Background(), "test/record", f.path, func(conn *sql.Conn, raw []byte) error {
		if _, err := conn.ExecContext(context.Background(), "DELETE FROM things"); err != nil {
			return err
		}
		_, err := conn.ExecContext(context.Background(), "INSERT INTO things(name) VALUES (?)", string(raw))
		return err
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return imported
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

// The copy is written after the commit and stamped, so our own copy is never
// imported back.
func TestMirrorWritesTheCommittedRecordAndNeverImportsIt(t *testing.T) {
	f := newMirrorFixture(t)
	f.commit("v1")
	f.mirror()
	if got := f.file(); got != "v1" {
		t.Fatalf("copy = %q, want v1", got)
	}
	if f.importFile() {
		t.Fatal("our own copy was imported")
	}
}

// A crash between the commit and the copy leaves the old copy with its
// stamp, so the newer record wins.
func TestCrashBeforeTheCopyKeepsTheRecord(t *testing.T) {
	f := newMirrorFixture(t)
	f.commit("v1")
	f.mirror()
	f.commit("v2") // The process stops before Mirror.
	if f.importFile() {
		t.Fatal("the stale copy replaced the newer record")
	}
	if got := f.record(); got != "v2" {
		t.Fatalf("record = %q, want v2", got)
	}
}

// An old binary's write is imported once.
func TestForeignWriteIsImportedOnce(t *testing.T) {
	f := newMirrorFixture(t)
	f.commit("v1")
	f.mirror()
	f.oldBinaryWrites("old")
	if !f.importFile() || f.record() != "old" {
		t.Fatalf("foreign write not imported: record = %q", f.record())
	}
	if f.importFile() {
		t.Fatal("the foreign write was imported twice")
	}
}

// stopMirrorAt makes Mirror stop at step, as a crash there would, and
// restores it when the test ends.
func stopMirrorAt(t *testing.T, step string) {
	t.Cleanup(func() { mirrorStep = func(string, string) error { return nil } })
	mirrorStep = func(at, _ string) error {
		if at == step {
			return errCrash
		}
		return nil
	}
}

var errCrash = errors.New("crash")

// mirrorCrashes runs one Mirror that stops where stopMirrorAt says.
func (f *mirrorFixture) mirrorCrashes() {
	f.t.Helper()
	if err := f.w.Mirror(context.Background(), "test/record", f.path, func(out io.Writer) error {
		_, err := io.WriteString(out, f.record())
		return err
	}); !errors.Is(err, errCrash) {
		f.t.Fatalf("Mirror = %v, want the crash", err)
	}
}

// A crash after the pending stamp and before the rename leaves the old
// copy, which still matches its stamp: the newer record wins.
func TestCrashBeforeTheRenameKeepsTheRecord(t *testing.T) {
	f := newMirrorFixture(t)
	f.commit("v1")
	f.mirror()
	f.commit("v2")
	stopMirrorAt(t, "pending")
	f.mirrorCrashes()
	if f.importFile() || f.record() != "v2" {
		t.Fatalf("the old copy replaced the record: record = %q", f.record())
	}
}

// A crash after the rename and before the stamp leaves our new copy, which
// matches pending. A newer record from another writer is not overwritten
// by it.
func TestCrashAfterTheRenameKeepsANewerRecord(t *testing.T) {
	f := newMirrorFixture(t)
	f.commit("v1")
	f.mirror()
	f.commit("v2")
	stopMirrorAt(t, "renamed")
	f.mirrorCrashes()
	f.commit("v3") // Another writer, before its own Mirror.
	if f.importFile() || f.record() != "v3" {
		t.Fatalf("our own copy replaced a newer record: record = %q", f.record())
	}
}

// An old binary's write at either crash point is imported once: Mirror
// neither renames over it nor stamps it as ours.
func TestForeignWriteAtEitherCrashPointIsImportedOnce(t *testing.T) {
	for _, step := range []string{"pending", "renamed"} {
		t.Run(step, func(t *testing.T) {
			f := newMirrorFixture(t)
			f.commit("v1")
			f.mirror()
			f.commit("v2")
			t.Cleanup(func() { mirrorStep = func(string, string) error { return nil } })
			mirrorStep = func(at, _ string) error {
				if at == step {
					f.oldBinaryWrites("old")
				}
				return nil
			}
			f.mirror()
			if !f.importFile() || f.record() != "old" {
				t.Fatalf("the old binary's write was lost: record = %q", f.record())
			}
			if f.importFile() {
				t.Fatal("the old binary's write was imported twice")
			}
		})
	}
}

// Two readers that race over one foreign write import it once.
func TestRacingReadersImportOnce(t *testing.T) {
	f := newMirrorFixture(t)
	f.commit("v1")
	f.mirror()
	f.oldBinaryWrites("old")
	var wg sync.WaitGroup
	imports := make(chan bool, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			imported, err := f.w.ImportIfForeign(context.Background(), "test/record", f.path, func(conn *sql.Conn, raw []byte) error {
				_, err := conn.ExecContext(context.Background(), "UPDATE things SET name = ?", string(raw))
				return err
			})
			if err != nil {
				t.Error(err)
			}
			imports <- imported
		}()
	}
	wg.Wait()
	close(imports)
	n := 0
	for imported := range imports {
		if imported {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d imports, want 1", n)
	}
}

// A symlink or other non-regular file is refused, not followed.
func TestImportRefusesANonRegularFile(t *testing.T) {
	f := newMirrorFixture(t)
	f.commit("v1")
	target := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, f.path); err != nil {
		t.Fatal(err)
	}
	if _, err := f.w.ImportIfForeign(context.Background(), "test/record", f.path, func(*sql.Conn, []byte) error {
		t.Fatal("a symlink was read")
		return nil
	}); err == nil {
		t.Fatal("a symlink was accepted")
	}
}

// ForgetMirrors drops a key's stamp and the stamps under it, in the store's
// transaction, and keeps a key that only shares its prefix.
func TestForgetMirrorsDropsAKeyAndItsChildren(t *testing.T) {
	f := newMirrorFixture(t)
	f.commit("v1")
	dir := t.TempDir()
	copies := map[string]string{"test/record": f.path, "test/record/child": filepath.Join(dir, "child.json"), "test/recorder": filepath.Join(dir, "recorder.json")}
	for key, path := range copies {
		if err := f.w.Mirror(context.Background(), key, path, func(out io.Writer) error {
			_, err := io.WriteString(out, "v1")
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.w.Write(context.Background(), hub.Event{}, func(conn *sql.Conn) error {
		return ForgetMirrors(context.Background(), conn, "test/record")
	}); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]bool{"test/record": true, "test/record/child": true, "test/recorder": false} {
		imported, err := f.w.ImportIfForeign(context.Background(), key, copies[key], func(*sql.Conn, []byte) error { return nil })
		if err != nil || imported != want {
			t.Errorf("%s: imported = %v, %v; want %v", key, imported, err, want)
		}
	}
}

// Only the engine changes the schema (item 41): before MigrateMirrors, Mirror
// and ForgetMirrors fail and create no table.
func TestMirrorsNeedTheEngineMigration(t *testing.T) {
	w, _ := mirrorWriter(t)
	path := filepath.Join(t.TempDir(), "record.json")
	if err := w.Mirror(context.Background(), "test/record", path, func(out io.Writer) error {
		_, err := io.WriteString(out, "v1")
		return err
	}); err == nil {
		t.Fatal("Mirror stamped a copy on an unmigrated database")
	}
	if err := w.Write(context.Background(), hub.Event{}, func(conn *sql.Conn) error {
		return ForgetMirrors(context.Background(), conn, "test/record")
	}); err == nil {
		t.Fatal("ForgetMirrors ran on an unmigrated database")
	}
	var n int
	if err := w.db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE name = 'state_mirrors'").Scan(&n); err != nil || n != 0 {
		t.Fatalf("state_mirrors tables = %d, %v; want none", n, err)
	}
}

// A file the read cannot use is skipped: the record stays, the file is
// logged once and stamped, so it is not tried again, and a later write by
// another binary is still imported.
func TestSkippedForeignFileIsStampedAndLoggedOnce(t *testing.T) {
	f := newMirrorFixture(t)
	// Only the skip line counts: a busy host also logs slow writes.
	var logged []string
	f.w.Logf = func(format string, args ...any) {
		if line := fmt.Sprintf(format, args...); strings.HasPrefix(line, "state: skipped ") {
			logged = append(logged, line)
		}
	}
	f.commit("v1")
	f.mirror()
	f.oldBinaryWrites("damaged")
	skip := func(conn *sql.Conn, raw []byte) error {
		if string(raw) == "damaged" {
			return fmt.Errorf("%w: not usable", ErrSkipForeign)
		}
		_, err := conn.ExecContext(context.Background(), "UPDATE things SET name = ?", string(raw))
		return err
	}
	for range 2 {
		imported, err := f.w.ImportIfForeign(context.Background(), "test/record", f.path, skip)
		if err != nil || imported {
			t.Fatalf("damaged file: imported %v, %v; want skipped", imported, err)
		}
	}
	if f.record() != "v1" || len(logged) != 1 {
		t.Fatalf("record %q after %d logs %q; want v1, logged once", f.record(), len(logged), logged)
	}
	f.oldBinaryWrites("v2")
	if imported, err := f.w.ImportIfForeign(context.Background(), "test/record", f.path, skip); err != nil || !imported || f.record() != "v2" {
		t.Fatalf("a later valid write: imported %v, %v, record %q; want v2", imported, err, f.record())
	}
}

// A directory or symlink at path makes Mirror fail, so the store reports a
// broken copy instead of keeping it as another binary's file. The refusal
// comes first: no temporary file is written and pending is not changed,
// since a pending inode left behind could match a later file.
func TestMirrorRefusesANonRegularPath(t *testing.T) {
	f := newMirrorFixture(t)
	f.commit("v1")
	f.mirror()
	before, err := readStamp(context.Background(), f.w.db, "test/record")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(f.path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(f.path, 0o700); err != nil {
		t.Fatal(err)
	}
	wrote := false
	if err := f.w.Mirror(context.Background(), "test/record", f.path, func(out io.Writer) error {
		wrote = true
		_, err := io.WriteString(out, "v1")
		return err
	}); err == nil {
		t.Fatal("Mirror kept a directory as the copy")
	}
	after, err := readStamp(context.Background(), f.w.db, "test/record")
	if err != nil {
		t.Fatal(err)
	}
	temps, _ := filepath.Glob(filepath.Join(filepath.Dir(f.path), ".record.json.mirror-*"))
	if wrote || len(temps) != 0 || after != before {
		t.Fatalf("refusal wrote a temporary file (%v, %v) or changed the stamp from %+v to %+v", wrote, temps, before, after)
	}
}
