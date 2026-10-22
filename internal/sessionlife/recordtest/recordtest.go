// Package recordtest writes and reads session records for tests in other
// packages, so their fixtures follow the record wherever sessionlife keeps it.
package recordtest

import (
	"os"
	"path/filepath"
	"testing"

	"dyna-agent/internal/sessionlife"
)

// Write stores v as session id's record under sessions, creating the
// session folder.
func Write(t testing.TB, sessions, id string, v any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(sessions, id), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := sessionlife.Save(sessions, id, v); err != nil {
		t.Fatal(err)
	}
}

// WriteRaw stores raw bytes as session id's record, valid or not, for tests
// of damaged records. They go to the legacy session.json, which is read while
// the session has no row, so any row is removed first.
func WriteRaw(t testing.TB, sessions, id string, raw []byte) {
	t.Helper()
	if err := sessionlife.Delete(sessions, id); err != nil {
		t.Fatal(err)
	}
	path := Path(t, sessions, id)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

// ReadRaw returns session id's record as stored, like os.ReadFile.
func ReadRaw(t testing.TB, sessions, id string) ([]byte, error) {
	t.Helper()
	raw, _, err := sessionlife.LoadRaw(sessions, id)
	return raw, err
}

// Remove deletes session id's record: its row and any legacy file.
func Remove(t testing.TB, sessions, id string) {
	t.Helper()
	if err := sessionlife.Delete(sessions, id); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(Path(t, sessions, id)); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

// Read decodes session id's record into v.
func Read(t testing.TB, sessions, id string, v any) {
	t.Helper()
	if err := sessionlife.Load(sessions, id, v); err != nil {
		t.Fatal(err)
	}
}

// Path is session id's legacy session.json, for tests of the legacy reader
// that damage, replace or inspect the file itself.
func Path(t testing.TB, sessions, id string) string {
	t.Helper()
	return filepath.Join(sessions, id, "session.json")
}
